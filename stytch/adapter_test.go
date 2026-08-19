package stytch

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic test timing.
type fakeClock struct {
	now atomic.Value // stores time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.now.Store(t)
	return c
}

func (c *fakeClock) Now() time.Time {
	return c.now.Load().(time.Time)
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now.Store(c.Now().Add(d))
}

// testConfig returns a Config suitable for unit tests.
func testConfig(clock Clock) Config {
	return Config{
		ProjectID:     "project-test-123",
		Secret:        "secret-test-abc",
		PublicToken:   "public-test-xyz",
		Environment:   "test",
		WebhookSecret: "whsec_test123",
		Timeout:       5 * time.Second,
		MaxAttempts:   3,
		Clock:         clock,
	}
}

func TestNew_ValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing ProjectID",
			cfg:     Config{Secret: "s", PublicToken: "p", Environment: "test", WebhookSecret: "whsec_x"},
			wantErr: "ProjectID",
		},
		{
			name:    "missing Secret",
			cfg:     Config{ProjectID: "p", PublicToken: "p", Environment: "test", WebhookSecret: "whsec_x"},
			wantErr: "Secret",
		},
		{
			name:    "missing PublicToken",
			cfg:     Config{ProjectID: "p", Secret: "s", Environment: "test", WebhookSecret: "whsec_x"},
			wantErr: "PublicToken",
		},
		{
			name:    "missing Environment",
			cfg:     Config{ProjectID: "p", Secret: "s", PublicToken: "p", WebhookSecret: "whsec_x"},
			wantErr: "Environment",
		},
		{
			name:    "missing WebhookSecret",
			cfg:     Config{ProjectID: "p", Secret: "s", PublicToken: "p", Environment: "test"},
			wantErr: "WebhookSecret",
		},
		{
			name:    "whitespace-only ProjectID",
			cfg:     Config{ProjectID: "   ", Secret: "s", PublicToken: "p", Environment: "test", WebhookSecret: "whsec_x"},
			wantErr: "ProjectID",
		},
		{
			name: "all valid",
			cfg:  Config{ProjectID: "p", Secret: "s", PublicToken: "p", Environment: "test", WebhookSecret: "whsec_x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "TestOp", false, func(ctx context.Context) error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	health := adapter.Health()
	if health.Op != "TestOp" {
		t.Errorf("health.Op = %q, want %q", health.Op, "TestOp")
	}
	if health.Outcome != "ok" {
		t.Errorf("health.Outcome = %q, want %q", health.Outcome, "ok")
	}
}

func TestDo_RetriesOnServerError(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	cfg.Timeout = 5 * time.Second
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "RetryOp", false, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return &Error{Kind: KindUnavailable, Op: "RetryOp", Status: 503, err: errors.New("service unavailable")}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}

	health := adapter.Health()
	if health.Outcome != "ok" {
		t.Errorf("health.Outcome = %q, want %q", health.Outcome, "ok")
	}
}

func TestDo_ExhaustsRetriesOnPersistentFailure(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "ExhaustOp", false, func(ctx context.Context) error {
		calls++
		return &Error{Kind: KindUnavailable, Op: "ExhaustOp", Status: 500, err: errors.New("internal error")}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (max attempts), got %d", calls)
	}

	var stytchErr *Error
	if !errors.As(err, &stytchErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if stytchErr.Kind != KindUnavailable {
		t.Errorf("expected KindUnavailable, got %v", stytchErr.Kind)
	}

	health := adapter.Health()
	if health.Outcome != "error" {
		t.Errorf("health.Outcome = %q, want %q", health.Outcome, "error")
	}
	if health.Status != 500 {
		t.Errorf("health.Status = %d, want %d", health.Status, 500)
	}
}

func TestDo_NeverRetriesMutatingWithoutIdempotencyKey(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "MutatingOp", true, func(ctx context.Context) error {
		calls++
		return &Error{Kind: KindUnavailable, Op: "MutatingOp", Status: 500, err: errors.New("server error")}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Req 20.2: mutating calls are never retried.
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry for mutating), got %d", calls)
	}
}

func TestDo_Retries429WithRetryAfter(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "RateLimitOp", false, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return &Error{
				Kind:       KindRateLimited,
				Op:         "RateLimitOp",
				Status:     429,
				RetryAfter: 100 * time.Millisecond,
				err:        errors.New("rate limited"),
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ClampsRetryAfterToTimeout(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	cfg.Timeout = 1 * time.Second
	adapter := NewWithClient(cfg, nil)

	// The retry delay for a 429 should be clamped to Timeout.
	delay := adapter.retryDelay(&Error{Status: 429, RetryAfter: 10 * time.Second}, 1)
	if delay != 1*time.Second {
		t.Errorf("expected delay clamped to 1s, got %v", delay)
	}
}

func TestDo_AppliesPerAttemptTimeout(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	cfg.Timeout = 100 * time.Millisecond
	cfg.MaxAttempts = 1
	adapter := NewWithClient(cfg, nil)

	err := adapter.Do(context.Background(), "TimeoutOp", false, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("expected context to have a deadline")
			return nil
		}
		// The deadline should be approximately 100ms from now.
		remaining := time.Until(deadline)
		if remaining > 150*time.Millisecond {
			t.Errorf("deadline too far: %v", remaining)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDo_DoesNotRetryClientErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "ClientErrOp", false, func(ctx context.Context) error {
		calls++
		return &Error{Kind: KindInvalidRequest, Op: "ClientErrOp", Status: 400, err: errors.New("bad request")}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry for 4xx), got %d", calls)
	}
}

func TestDo_DoesNotRetry401(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	calls := 0
	err := adapter.Do(context.Background(), "AuthOp", false, func(ctx context.Context) error {
		calls++
		return &Error{Kind: KindUnauthenticated, Op: "AuthOp", Status: 401, err: errors.New("unauthorized")}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry for 401), got %d", calls)
	}
}

func TestDo_ErrorMessageExcludesSecrets(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	cfg.Secret = "super-secret-key-12345"
	adapter := NewWithClient(cfg, nil)

	// Create an error whose message contains the secret.
	err := adapter.Do(context.Background(), "SecretOp", true, func(ctx context.Context) error {
		return &Error{
			Kind:   KindUnavailable,
			Op:     "SecretOp",
			Status: 500,
			err:    errors.New("failed with super-secret-key-12345 in message"),
		}
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// The error message from the Error type itself must not contain the secret.
	var stytchErr *Error
	if errors.As(err, &stytchErr) {
		// The Error.Error() method must not include the secret.
		if strings.Contains(stytchErr.Error(), "super-secret-key-12345") {
			t.Error("Error.Error() contains the secret — violates Req 20.12")
		}
	}
}

func TestHealth_ReturnsZeroValueBeforeAnyCall(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	health := adapter.Health()
	if health.Op != "" {
		t.Errorf("expected empty Op before any call, got %q", health.Op)
	}
	if health.Outcome != "" {
		t.Errorf("expected empty Outcome before any call, got %q", health.Outcome)
	}
}

func TestHealth_UpdatedAfterSuccessAndFailure(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	// Success.
	_ = adapter.Do(context.Background(), "SuccessOp", false, func(ctx context.Context) error {
		return nil
	})

	health := adapter.Health()
	if health.Op != "SuccessOp" || health.Outcome != "ok" {
		t.Errorf("after success: Op=%q Outcome=%q, want SuccessOp/ok", health.Op, health.Outcome)
	}
	if !health.At.Equal(clock.Now()) {
		t.Errorf("health.At = %v, want %v", health.At, clock.Now())
	}

	// Failure.
	clock.Advance(time.Minute)
	_ = adapter.Do(context.Background(), "FailOp", true, func(ctx context.Context) error {
		return &Error{Kind: KindUnavailable, Op: "FailOp", Status: 502}
	})

	health = adapter.Health()
	if health.Op != "FailOp" || health.Outcome != "error" {
		t.Errorf("after failure: Op=%q Outcome=%q, want FailOp/error", health.Op, health.Outcome)
	}
	if health.Status != 502 {
		t.Errorf("health.Status = %d, want 502", health.Status)
	}
}

func TestContextWithWorkspaceID(t *testing.T) {
	ctx := context.Background()
	ctx = ContextWithWorkspaceID(ctx, "ws-123")

	got := workspaceIDFromContext(ctx)
	if got != "ws-123" {
		t.Errorf("workspaceIDFromContext = %q, want %q", got, "ws-123")
	}
}

func TestContextWithWorkspaceID_EmptyIsNoOp(t *testing.T) {
	ctx := context.Background()
	ctx = ContextWithWorkspaceID(ctx, "")

	got := workspaceIDFromContext(ctx)
	if got != "" {
		t.Errorf("workspaceIDFromContext = %q, want empty", got)
	}
}

func TestNewWithClient_AppliesDefaults(t *testing.T) {
	adapter := NewWithClient(Config{}, nil)

	if adapter.cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", adapter.cfg.Timeout)
	}
	if adapter.cfg.JWKSTTL != time.Hour {
		t.Errorf("JWKSTTL = %v, want 1h", adapter.cfg.JWKSTTL)
	}
	if adapter.cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", adapter.cfg.MaxAttempts)
	}
	if adapter.cfg.Clock == nil {
		t.Error("Clock should not be nil after defaults applied")
	}
}

// TestDo_StopsRetryingWhenContextCancelledDuringBackoff pins the fix for a
// `break` that only left the enclosing select: on cancellation during the
// backoff wait the loop used to continue, spending another attempt against an
// already-dead context and overwriting lastErr with that attempt's error.
func TestDo_StopsRetryingWhenContextCancelledDuringBackoff(t *testing.T) {
	clock := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := testConfig(clock)
	adapter := NewWithClient(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	calls := 0
	err := adapter.Do(ctx, "CancelDuringBackoff", false, func(context.Context) error {
		calls++
		// Cancel from a goroutine so the parent context is still live at the
		// pre-wait ctx.Err() guard and the cancellation lands inside the
		// backoff select instead.
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		return &Error{Kind: KindUnavailable, Op: "CancelDuringBackoff", Status: 503, err: errors.New("service unavailable")}
	})

	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("expected the loop to stop after 1 attempt, got %d", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the returned error to wrap context.Canceled, got %v", err)
	}
}

// jwksFetchTimeoutFor is applied both by applyDefaults and directly inside
// FetchJWKS, since callers pass a raw Config to the latter. An over-large value
// must fall back to the default rather than be honoured: the fetch is held
// under the JWKS cache write lock, so it stalls session verification for every
// workspace.
func TestJWKSFetchTimeoutIsClamped(t *testing.T) {
	tests := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{name: "unset falls back to the default", set: 0, want: defaultJWKSFetchTimeout},
		{name: "negative falls back to the default", set: -time.Second, want: defaultJWKSFetchTimeout},
		{name: "a value within the bound is honoured", set: 20 * time.Second, want: 20 * time.Second},
		{name: "the bound itself is honoured", set: maxJWKSFetchTimeout, want: maxJWKSFetchTimeout},
		{name: "above the bound falls back to the default", set: time.Hour, want: defaultJWKSFetchTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jwksFetchTimeoutFor(Config{JWKSFetchTimeout: tt.set}); got != tt.want {
				t.Fatalf("jwksFetchTimeoutFor(%v) = %v, want %v", tt.set, got, tt.want)
			}

			cfg := testConfig(newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)))
			cfg.JWKSFetchTimeout = tt.set
			applyDefaults(&cfg)
			if cfg.JWKSFetchTimeout != tt.want {
				t.Fatalf("applyDefaults left JWKSFetchTimeout = %v, want %v", cfg.JWKSFetchTimeout, tt.want)
			}
		})
	}
}
