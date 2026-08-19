package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// RateLimiter is implemented by both the fixed-window Limiter and the
// SlidingWindowLimiter, letting callers depend on whichever implementation is
// wired at startup without a type-specific constructor signature.
type RateLimiter interface {
	Allow(ctx context.Context, n Name, subject string) Decision
	Guard(n Name, subject func(echo.Context) string) echo.MiddlewareFunc
}

// Name identifies a named rate-limit bucket from the table.
type Name string

const (
	LimitSignupPerIP      Name = "signup_ip"        // 5 / 10m  (Req 25.1)
	LimitSignupPerEmail   Name = "signup_email"     // 3 / 10m  (Req 25.2)
	LimitLinkChallenge    Name = "link_challenge"   // 5 / 1h   (Req 5.5)
	LimitInviteCreate     Name = "invite_create"    // 20 / 1h  (Req 10.12)
	LimitInviteResend     Name = "invite_resend"    // 3 / 1h   (Req 12.4)
	LimitInviteResolveMin Name = "invite_resolve_m" // 10 / 1m  (Req 11.18)
	LimitInviteResolveHr  Name = "invite_resolve_h" // 30 / 1h  (Req 25.6)
	LimitWorkspaceCreate  Name = "workspace_create" // 5 / 24h  (Req 15.13)
	LimitWebhookPerIP     Name = "webhook_ip"       // 120 / 1m (public endpoint DoS guard)
)

// Limit defines the maximum allowed requests within a fixed time window.
type Limit struct {
	Max    int
	Window time.Duration
}

// limits is the authoritative named-limit table.
var limits = map[Name]Limit{
	LimitSignupPerIP:      {Max: 5, Window: 10 * time.Minute},
	LimitSignupPerEmail:   {Max: 3, Window: 10 * time.Minute},
	LimitLinkChallenge:    {Max: 5, Window: time.Hour},
	LimitInviteCreate:     {Max: 20, Window: time.Hour},
	LimitInviteResend:     {Max: 3, Window: time.Hour},
	LimitInviteResolveMin: {Max: 10, Window: time.Minute},
	LimitInviteResolveHr:  {Max: 30, Window: time.Hour},
	LimitWorkspaceCreate:  {Max: 5, Window: 24 * time.Hour},
	LimitWebhookPerIP:     {Max: 120, Window: time.Minute},
}

// GetLimit returns the Limit for a given Name and whether it exists.
func GetLimit(n Name) (Limit, bool) {
	l, ok := limits[n]
	return l, ok
}

// Decision is the result of an Allow call.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Limit      Limit
}

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// memEntry is one counter in the in-memory fallback store.
type memEntry struct {
	count     int
	expiresAt time.Time
}

// Limiter implements a fixed-window rate limiter backed by Redis or an
// in-process map when Redis is unavailable (Req 25.9).
type Limiter struct {
	rdb   *redis.Client // nil → in-memory fallback
	clock Clock
	mu    sync.Mutex
	mem   map[string]*memEntry
}

// NewLimiter constructs a Limiter. Pass nil for rdb to use the in-memory
// fallback (single-instance or test). Pass nil for clock to use real time.
func NewLimiter(rdb *redis.Client, clock Clock) *Limiter {
	if clock == nil {
		clock = systemClock{}
	}
	return &Limiter{
		rdb:   rdb,
		clock: clock,
		mem:   make(map[string]*memEntry),
	}
}

// Allow checks whether the operation identified by (name, subject) is allowed
// under the fixed-window counter for the named limit.
//
// The fixed window is computed as now.Unix() / window_seconds, so at most 2×Max
// requests can succeed across a window boundary — the accepted reading.
//
// Store is Redis when rdb is non-nil, in-process map otherwise.
func (l *Limiter) Allow(ctx context.Context, n Name, subject string) Decision {
	lim, ok := limits[n]
	if !ok {
		// Unknown limit name — allow by default (should not happen in practice).
		return Decision{Allowed: true, Limit: lim}
	}

	now := l.clock.Now()
	windowSec := int64(lim.Window.Seconds())
	windowNum := now.Unix() / windowSec
	key := fmt.Sprintf("rl:%s:%s:%d", string(n), subject, windowNum)

	if l.rdb != nil {
		return l.allowRedis(ctx, key, lim, now, windowSec, windowNum)
	}
	return l.allowMem(key, lim, now)
}

// allowRedis uses a Redis pipeline: INCR + conditional EXPIRE.
func (l *Limiter) allowRedis(ctx context.Context, key string, lim Limit, now time.Time, windowSec, windowNum int64) Decision {
	pipe := l.rdb.Pipeline()
	incrCmd := pipe.Incr(ctx, key)
	// Set TTL only when the key has no expiry yet (first increment).
	// TTL returns -2 for missing keys and -1 for keys with no expiry,
	// but since INCR creates the key, we just always set expire with NX semantics.
	pipe.ExpireNX(ctx, key, lim.Window)
	_, _ = pipe.Exec(ctx)

	count := int(incrCmd.Val())
	if count > lim.Max {
		// Calculate time remaining in this window.
		windowStart := windowNum * windowSec
		windowEnd := windowStart + windowSec
		retryAfter := time.Duration(windowEnd-now.Unix()) * time.Second
		if retryAfter < 0 {
			retryAfter = 0
		}
		return Decision{
			Allowed:    false,
			RetryAfter: retryAfter,
			Limit:      lim,
		}
	}

	return Decision{Allowed: true, Limit: lim}
}

// allowMem uses the in-process map with a mutex.
func (l *Limiter) allowMem(key string, lim Limit, now time.Time) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, exists := l.mem[key]
	if !exists || now.After(entry.expiresAt) {
		// New window: create or reset entry.
		l.mem[key] = &memEntry{
			count:     1,
			expiresAt: now.Add(lim.Window),
		}
		return Decision{Allowed: true, Limit: lim}
	}

	entry.count++
	if entry.count > lim.Max {
		retryAfter := entry.expiresAt.Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
		return Decision{
			Allowed:    false,
			RetryAfter: retryAfter,
			Limit:      lim,
		}
	}

	return Decision{Allowed: true, Limit: lim}
}
