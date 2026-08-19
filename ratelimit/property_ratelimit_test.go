package ratelimit

import (
	"context"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 26: Rate limits never admit more than the stated count
// Validates: Requirements 10.12, 10.13, 12.4, 15.13, 25.1, 25.2, 25.6
//
// Uses the in-memory fallback (rdb=nil) since no Redis instance is available
// in this environment; the fixed-window Allow logic under test is identical
// on both storage paths (allowRedis and allowMem apply the same window
// arithmetic), so this exercises the real windowing algorithm.
func TestProperty26_RateLimitsNeverAdmitMoreThanStatedCount(t *testing.T) {
	names := make([]Name, 0, len(limits))
	for n := range limits {
		names = append(names, n)
	}

	rapid.Check(t, func(t *rapid.T) {
		name := rapid.SampledFrom(names).Draw(t, "limitName")
		lim, ok := GetLimit(name)
		if !ok {
			t.Fatalf("no Limit for name %s", name)
		}

		// Anchor at the start of a fixed window (an exact multiple of the
		// window size in Unix seconds) so every clock advance below, bounded
		// to stay under the window size, can never cross a window boundary —
		// crossing one legitimately resets the count under fixed-window
		// semantics (the documented, accepted up-to-2x-Max boundary
		// admission), which is a different property than this one.
		windowStart := time.Unix(0, 0).UTC()
		clock := &fakeClock{now: windowStart}
		limiter := NewLimiter(nil, clock)
		subject := "subject-under-test"

		burst := rapid.IntRange(1, 200).Draw(t, "burstSize")
		maxTotalAdvance := lim.Window - time.Second
		if maxTotalAdvance < 0 {
			maxTotalAdvance = 0
		}

		admitted := 0
		remainingAdvance := maxTotalAdvance
		for i := 0; i < burst; i++ {
			if remainingAdvance > 0 && rapid.Bool().Draw(t, "advanceClock") {
				step := time.Duration(rapid.Int64Range(0, int64(remainingAdvance)).Draw(t, "advanceAmount"))
				clock.Advance(step)
				remainingAdvance -= step
			}

			decision := limiter.Allow(context.Background(), name, subject)
			if decision.Allowed {
				admitted++
			} else if decision.RetryAfter < 0 {
				t.Fatalf("RetryAfter must not be negative, got %v", decision.RetryAfter)
			}
		}

		if admitted > lim.Max {
			t.Fatalf("limit %s: admitted %d requests in one window, want <= %d", name, admitted, lim.Max)
		}
	})
}

// Feature: stytch-employer-onboarding, Property 26b: rejections always carry a valid Retry-After
// Validates: Requirement 25.3
func TestProperty26_RejectionsCarryValidRetryAfter(t *testing.T) {
	names := make([]Name, 0, len(limits))
	for n := range limits {
		names = append(names, n)
	}

	rapid.Check(t, func(t *rapid.T) {
		name := rapid.SampledFrom(names).Draw(t, "limitName")
		lim, _ := GetLimit(name)

		clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
		limiter := NewLimiter(nil, clock)
		subject := "subject-b"

		var lastDecision Decision
		for i := 0; i < lim.Max+5; i++ {
			lastDecision = limiter.Allow(context.Background(), name, subject)
		}

		if lastDecision.Allowed {
			t.Fatalf("expected the (Max+5)th request to be rejected for %s", name)
		}
		if lastDecision.RetryAfter < 0 {
			t.Fatalf("RetryAfter must not be negative, got %v", lastDecision.RetryAfter)
		}
	})
}
