package stytch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"time"
)

// HealthSnapshot is the last-call outcome exposed by the adapter's Health method.
// It is stored via an atomic.Pointer so reads are lock-free (Req 20.13).
type HealthSnapshot struct {
	Op      string    `json:"op"`
	Outcome string    `json:"outcome"` // "ok" or "error"
	Status  int       `json:"status"`
	At      time.Time `json:"at"`
}

// Adapter wraps a Client with the retry, timeout, classification, health, and
// logging policy of Req 20. Every domain service depends on *Adapter, not Client.
type Adapter struct {
	client Client
	cfg    Config
	health atomic.Pointer[HealthSnapshot]
	jwks   *jwksCache
}

// workspaceIDKey is the context key for carrying the workspace identifier into
// adapter calls for structured error logging (Req 20.9). The adapter itself
// never sets this value; the session resolver or the calling domain service does.
type workspaceIDKey struct{}

// ContextWithWorkspaceID returns a copy of ctx carrying the workspace identifier
// for adapter error logging. Domain services call this before issuing adapter
// operations under a resolved session.
func ContextWithWorkspaceID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceIDKey{}, id)
}

// workspaceIDFromContext extracts the workspace identifier from ctx, if present.
func workspaceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(workspaceIDKey{}).(string)
	return id
}

// New validates cfg and returns a ready-to-use Adapter. It is called from main.go.
// Validation fails startup with a descriptive error naming the missing field,
// satisfying Req 20.11.
func New(cfg Config) (*Adapter, error) {
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	// The real HTTP client will be created by a later task (3.5).
	// For now, New requires that the caller provides a client via NewWithClient
	// or this returns an adapter with a nil client that will panic on use.
	return &Adapter{
		client: nil,
		cfg:    cfg,
	}, nil
}

// JWKSSource is an optional interface that a Client can implement to provide
// JWKS bytes directly (e.g. the Fake). When the Client does not implement it,
// the adapter must be given a JWKS fetch function via NewWithJWKSFetch.
type JWKSSource interface {
	JWKS() []byte
}

// NewWithClient creates an Adapter with the given Client. This is the primary
// test seam: tests pass a Fake and never touch the network.
// If the Client implements JWKSSource, the JWKS cache is initialized automatically.
func NewWithClient(cfg Config, c Client) *Adapter {
	applyDefaults(&cfg)
	a := &Adapter{
		client: c,
		cfg:    cfg,
	}

	// If the client can provide JWKS bytes directly (e.g. the Fake), wire the cache.
	if src, ok := c.(JWKSSource); ok {
		a.jwks = newJWKSCache(cfg.JWKSTTL, cfg.Clock, func() ([]byte, error) {
			return src.JWKS(), nil
		})
		// Pre-populate the cache so VerifySessionJWT works immediately.
		_ = a.jwks.Refresh()
	}

	return a
}

// NewWithJWKSFetch creates an Adapter with an explicit JWKS fetch function.
// Used in production where JWKS is fetched over HTTP from Stytch.
func NewWithJWKSFetch(cfg Config, c Client, fetchJWKS func() ([]byte, error)) *Adapter {
	applyDefaults(&cfg)
	a := &Adapter{
		client: c,
		cfg:    cfg,
	}
	a.jwks = newJWKSCache(cfg.JWKSTTL, cfg.Clock, fetchJWKS)
	return a
}

// validateConfig checks that required fields are present and applies defaults.
func validateConfig(cfg *Config) error {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return fmt.Errorf("stytch: Config.ProjectID is required")
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return fmt.Errorf("stytch: Config.Secret is required")
	}
	if strings.TrimSpace(cfg.PublicToken) == "" {
		return fmt.Errorf("stytch: Config.PublicToken is required")
	}
	if strings.TrimSpace(cfg.Environment) == "" {
		return fmt.Errorf("stytch: Config.Environment is required")
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		return fmt.Errorf("stytch: Config.WebhookSecret is required")
	}
	applyDefaults(cfg)
	return nil
}

// applyDefaults fills in zero-value fields with their documented defaults.
func applyDefaults(cfg *Config) {
	if cfg.Timeout <= 0 || cfg.Timeout > 5*time.Second {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.JWKSTTL < time.Hour {
		cfg.JWKSTTL = time.Hour
	}
	// Clamped rather than merely defaulted: this bounds a fetch held under the
	// JWKS cache write lock, so an over-large value would block session
	// verification platform-wide for that long on a cold miss.
	cfg.JWKSFetchTimeout = jwksFetchTimeoutFor(*cfg)
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
}

// Do wraps one logical Stytch operation with retry, timeout, and error
// classification.
//
// Parameters:
//   - name: operation label for logs and health snapshots
//   - mutating: whether the call changes remote state without an idempotency key;
//     Req 20.2 forbids retrying such calls
//   - fn: the actual Stytch call. It receives a context with a per-attempt timeout.
//
// The retry schedule is:
//   - Attempt 1: immediate
//   - Attempt 2: after 200ms ± 20% jitter
//   - Attempt 3: after 600ms ± 20% jitter
//   - On 429: honour Retry-After clamped to Timeout, at most 3 total attempts
//   - Never retry mutating == true without an idempotency key (Req 20.2)
//
// On exhaustion, exactly one error log is emitted naming the operation, the
// final status, and the workspace identifier when available (Req 20.9). The log
// excludes every secret (Req 20.12).
func (a *Adapter) Do(ctx context.Context, name string, mutating bool, fn func(context.Context) error) error {
	maxAttempts := a.cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	var lastErr error
	var lastStatus int

retry:
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Apply per-attempt timeout (Req 20.1).
		attemptCtx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
		err := fn(attemptCtx)
		cancel()

		if err == nil {
			// Success: update health and return.
			a.recordHealth(name, "ok", 0)
			return nil
		}

		lastErr = err
		lastStatus = statusFromError(err)

		// Determine whether to retry.
		if !a.shouldRetry(err, mutating, attempt, maxAttempts) {
			break
		}

		// If parent context is already done, don't wait.
		if ctx.Err() != nil {
			break
		}

		// Compute delay.
		delay := a.retryDelay(err, attempt)

		// Wait for the delay or context cancellation. The label is required:
		// a bare `break` here would leave only the select and burn another
		// attempt against an already-cancelled context.
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			break retry
		case <-a.after(delay):
			// Continue to next attempt.
		}
	}

	// All attempts exhausted or no retry allowed.
	a.recordHealth(name, "error", lastStatus)
	a.logExhaustion(ctx, name, lastStatus, lastErr)

	// Wrap in a classified Error if not already one.
	var stytchErr *Error
	if errors.As(lastErr, &stytchErr) {
		return stytchErr
	}
	return &Error{
		Kind:   classifyRawError(lastErr, lastStatus),
		Op:     name,
		Status: lastStatus,
		err:    lastErr,
	}
}

// Health returns the most recent health snapshot. If no call has been made yet,
// it returns a zero-value HealthSnapshot.
func (a *Adapter) Health() HealthSnapshot {
	if snap := a.health.Load(); snap != nil {
		return *snap
	}
	return HealthSnapshot{}
}

// Client returns the underlying Client for domain services that need to compose
// calls (e.g. VerifySessionJWT delegates to the client for opaque token auth).
func (a *Adapter) Client() Client {
	return a.client
}

// Config returns the adapter's configuration. Useful for domain services that
// need non-secret values (e.g. Environment).
func (a *Adapter) Config() Config {
	return a.cfg
}

// shouldRetry decides whether the given error warrants another attempt.
func (a *Adapter) shouldRetry(err error, mutating bool, attempt, maxAttempts int) bool {
	if attempt >= maxAttempts {
		return false
	}

	// Req 20.2: NEVER retry mutating calls without an idempotency key.
	if mutating {
		return false
	}

	status := statusFromError(err)

	// Retry on 429 (rate limited).
	if status == 429 {
		return true
	}

	// Retry on 5xx (server error).
	if status >= 500 && status < 600 {
		return true
	}

	// Retry on network/timeout errors (no HTTP status).
	if status == 0 && !errors.Is(err, context.Canceled) {
		return true
	}

	return false
}

// retryDelay computes the delay before the next attempt.
// Base delays: attempt 1→2 is 200ms, attempt 2→3 is 600ms.
// For 429, honour Retry-After clamped to Timeout.
func (a *Adapter) retryDelay(err error, attempt int) time.Duration {
	// Check for 429 with Retry-After.
	if retryAfter := retryAfterFromError(err); retryAfter > 0 {
		if retryAfter > a.cfg.Timeout {
			retryAfter = a.cfg.Timeout
		}
		return retryAfter
	}

	// Exponential backoff: 200ms for first retry, 600ms for second.
	var base time.Duration
	switch attempt {
	case 1:
		base = 200 * time.Millisecond
	case 2:
		base = 600 * time.Millisecond
	default:
		base = 600 * time.Millisecond
	}

	// Apply ±20% jitter from the clock.
	jitter := a.jitter(base)
	return jitter
}

// jitter applies ±20% jitter to the base duration using the adapter's clock.
// The jitter is deterministic from the clock's current nanosecond, making tests
// reproducible when using a fake clock.
func (a *Adapter) jitter(base time.Duration) time.Duration {
	now := a.cfg.Clock.Now()
	// Use nanoseconds as a pseudo-random source for jitter.
	// This gives ±20% variation: multiply by [0.8, 1.2].
	nanos := now.UnixNano()
	// Map nanos to a value in [-0.2, +0.2].
	// Use the low bits for reasonable distribution.
	fraction := float64(nanos%1000) / 1000.0 // [0, 1)
	jitterFactor := 0.8 + (fraction * 0.4)   // [0.8, 1.2)
	return time.Duration(math.Round(float64(base) * jitterFactor))
}

// after returns a channel that fires after the given duration. It uses the
// system timer (not the injected clock) because the delay is a real wall-clock
// wait in production. Tests that need instant retries use a zero-delay by
// controlling the error's Retry-After or by setting MaxAttempts to 1.
func (a *Adapter) after(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// recordHealth atomically stores the latest health snapshot.
func (a *Adapter) recordHealth(op, outcome string, status int) {
	snap := &HealthSnapshot{
		Op:      op,
		Outcome: outcome,
		Status:  status,
		At:      a.cfg.Clock.Now(),
	}
	a.health.Store(snap)
}

// logExhaustion emits exactly one error log per exhausted operation (Req 20.9).
// It names the operation and the final status, includes the workspace identifier
// when the context carries one, and excludes every secret (Req 20.12).
func (a *Adapter) logExhaustion(ctx context.Context, name string, status int, err error) {
	attrs := []any{
		slog.String("op", name),
		slog.Int("status", status),
	}

	if wsID := workspaceIDFromContext(ctx); wsID != "" {
		attrs = append(attrs, slog.String("workspace_id", wsID))
	}

	// Include the error message but scrub secrets.
	if err != nil {
		msg := err.Error()
		msg = scrubSecrets(msg, a.cfg.Secret)
		attrs = append(attrs, slog.String("error", msg))
	}

	slog.ErrorContext(ctx, "stytch: operation exhausted all retries", attrs...)
}

// scrubSecrets removes any occurrence of the given secrets from a message.
// This ensures Req 20.12 compliance: no secret in any log.
func scrubSecrets(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" && strings.Contains(msg, s) {
			msg = strings.ReplaceAll(msg, s, "[REDACTED]")
		}
	}
	return msg
}

// statusFromError extracts the HTTP status from an *Error, or returns 0.
func statusFromError(err error) int {
	var stytchErr *Error
	if errors.As(err, &stytchErr) {
		return stytchErr.Status
	}
	return 0
}

// retryAfterFromError extracts a Retry-After duration from a rate-limit error.
// Returns 0 if no Retry-After is available.
func retryAfterFromError(err error) time.Duration {
	var stytchErr *Error
	if errors.As(err, &stytchErr) {
		if stytchErr.Status == 429 && stytchErr.RetryAfter > 0 {
			return stytchErr.RetryAfter
		}
	}
	return 0
}

// classifyRawError maps a non-*Error into a Kind based on the status code and
// error characteristics.
func classifyRawError(err error, status int) Kind {
	if status == 429 {
		return KindRateLimited
	}
	if status >= 500 && status < 600 {
		return KindUnavailable
	}
	if status == 404 {
		return KindNotFound
	}
	if status == 409 {
		return KindConflict
	}
	if status == 401 || status == 403 {
		return KindUnauthenticated
	}
	if status >= 400 && status < 500 {
		return KindInvalidRequest
	}
	// Network error or timeout with no status.
	return KindUnavailable
}
