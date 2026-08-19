package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeClock implements Clock for deterministic testing.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestGetLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   Name
		wantOK bool
		max    int
		window time.Duration
	}{
		{LimitSignupPerIP, true, 5, 10 * time.Minute},
		{LimitSignupPerEmail, true, 3, 10 * time.Minute},
		{LimitLinkChallenge, true, 5, time.Hour},
		{LimitInviteCreate, true, 20, time.Hour},
		{LimitInviteResend, true, 3, time.Hour},
		{LimitInviteResolveMin, true, 10, time.Minute},
		{LimitInviteResolveHr, true, 30, time.Hour},
		{LimitWorkspaceCreate, true, 5, 24 * time.Hour},
		{"unknown_limit", false, 0, 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			lim, ok := GetLimit(tt.name)
			if ok != tt.wantOK {
				t.Fatalf("GetLimit(%q) ok = %v, want %v", tt.name, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if lim.Max != tt.max {
				t.Errorf("Max = %d, want %d", lim.Max, tt.max)
			}
			if lim.Window != tt.window {
				t.Errorf("Window = %v, want %v", lim.Window, tt.window)
			}
		})
	}
}

func TestAllowInMemory_WithinLimit(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)
	ctx := context.Background()

	// LimitSignupPerIP allows 5 per 10 minutes
	for i := 0; i < 5; i++ {
		d := limiter.Allow(ctx, LimitSignupPerIP, "192.168.1.1")
		if !d.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
}

func TestAllowInMemory_ExceedsLimit(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)
	ctx := context.Background()

	// Exhaust the 5 allowed requests
	for i := 0; i < 5; i++ {
		limiter.Allow(ctx, LimitSignupPerIP, "192.168.1.1")
	}

	// 6th request should be denied
	d := limiter.Allow(ctx, LimitSignupPerIP, "192.168.1.1")
	if d.Allowed {
		t.Fatal("6th request should be denied")
	}
	if d.RetryAfter <= 0 {
		t.Error("RetryAfter should be positive when denied")
	}
	if d.Limit.Max != 5 {
		t.Errorf("Decision.Limit.Max = %d, want 5", d.Limit.Max)
	}
}

func TestAllowInMemory_WindowReset(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)
	ctx := context.Background()

	// Exhaust limits
	for i := 0; i < 5; i++ {
		limiter.Allow(ctx, LimitSignupPerIP, "10.0.0.1")
	}
	d := limiter.Allow(ctx, LimitSignupPerIP, "10.0.0.1")
	if d.Allowed {
		t.Fatal("should be denied after exhausting limit")
	}

	// Advance clock past the window
	clock.Advance(11 * time.Minute)

	// Should be allowed again in the new window
	d = limiter.Allow(ctx, LimitSignupPerIP, "10.0.0.1")
	if !d.Allowed {
		t.Fatal("should be allowed after window expires")
	}
}

func TestAllowInMemory_DifferentSubjects(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)
	ctx := context.Background()

	// Exhaust limit for subject A
	for i := 0; i < 5; i++ {
		limiter.Allow(ctx, LimitSignupPerIP, "subject_a")
	}
	d := limiter.Allow(ctx, LimitSignupPerIP, "subject_a")
	if d.Allowed {
		t.Fatal("subject_a should be denied")
	}

	// Subject B should still be allowed
	d = limiter.Allow(ctx, LimitSignupPerIP, "subject_b")
	if !d.Allowed {
		t.Fatal("subject_b should be allowed (independent counter)")
	}
}

func TestAllowInMemory_UnknownLimit(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)
	ctx := context.Background()

	d := limiter.Allow(ctx, "nonexistent_limit", "any_subject")
	if !d.Allowed {
		t.Fatal("unknown limit should default to allowed")
	}
}

func TestAllowRedis_WithinLimit(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(client, clock)
	ctx := context.Background()

	// LimitInviteResend allows 3 per hour
	for i := 0; i < 3; i++ {
		d := limiter.Allow(ctx, LimitInviteResend, "user123")
		if !d.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
}

func TestAllowRedis_ExceedsLimit(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(client, clock)
	ctx := context.Background()

	// Exhaust the 3 allowed requests for invite_resend
	for i := 0; i < 3; i++ {
		limiter.Allow(ctx, LimitInviteResend, "user456")
	}

	// 4th request should be denied
	d := limiter.Allow(ctx, LimitInviteResend, "user456")
	if d.Allowed {
		t.Fatal("4th request should be denied")
	}
	if d.RetryAfter <= 0 {
		t.Error("RetryAfter should be positive when denied")
	}
	if d.Limit.Max != 3 {
		t.Errorf("Decision.Limit.Max = %d, want 3", d.Limit.Max)
	}
}

func TestAllowRedis_KeyExpiry(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(client, clock)
	ctx := context.Background()

	// Use LimitInviteResolveMin: 10 per minute
	for i := 0; i < 10; i++ {
		limiter.Allow(ctx, LimitInviteResolveMin, "ip_addr")
	}
	d := limiter.Allow(ctx, LimitInviteResolveMin, "ip_addr")
	if d.Allowed {
		t.Fatal("should be denied after exhausting per-minute limit")
	}

	// Fast-forward miniredis past the TTL
	server.FastForward(2 * time.Minute)

	// New window (different window number), so a new key is used.
	// We also advance the clock so the window number changes.
	clock.Advance(2 * time.Minute)
	d = limiter.Allow(ctx, LimitInviteResolveMin, "ip_addr")
	if !d.Allowed {
		t.Fatal("should be allowed after window changes")
	}
}

func TestAllowRedis_DifferentSubjects(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(client, clock)
	ctx := context.Background()

	// Exhaust limit for one subject
	for i := 0; i < 3; i++ {
		limiter.Allow(ctx, LimitInviteResend, "user_x")
	}
	d := limiter.Allow(ctx, LimitInviteResend, "user_x")
	if d.Allowed {
		t.Fatal("user_x should be denied")
	}

	// Different subject should still be allowed
	d = limiter.Allow(ctx, LimitInviteResend, "user_y")
	if !d.Allowed {
		t.Fatal("user_y should be allowed (independent counter)")
	}
}

func TestNewLimiter_NilClockUsesSystemClock(t *testing.T) {
	t.Parallel()

	limiter := NewLimiter(nil, nil)
	ctx := context.Background()

	// Should not panic and should allow the first request
	d := limiter.Allow(ctx, LimitSignupPerIP, "test_subject")
	if !d.Allowed {
		t.Fatal("first request should be allowed with system clock")
	}
}
