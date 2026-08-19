package identity

import (
	"context"
	"testing"
	"time"

	"backend/ratelimit"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 25: Link_Challenge attempt limits are enforced exactly
// Validates: Requirements 5.4, 5.5
//
// Uses the real LinkService and the real in-memory rate limiter (rdb=nil):
// at most 5 Issue calls per Link_Candidate succeed within any one-hour
// window, and a challenge is never usable (Verify never succeeds) once now
// has passed issued_at+15m, regardless of how many Issue/Verify calls came
// before it.
func TestProperty25_LinkChallengeAttemptLimitsEnforcedExactly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		windowStart := time.Unix(0, 0).UTC()
		clock := &fakeClock{now: windowStart}
		limiter := ratelimit.NewLimiter(nil, clock)
		svc := NewLinkService(limiter, clock)
		candidateID := rapid.StringMatching(`cand-[0-9]{1,4}`).Draw(t, "candidateID")

		lim, ok := ratelimit.GetLimit(ratelimit.LimitLinkChallenge)
		if !ok {
			t.Fatalf("no Limit registered for %s", ratelimit.LimitLinkChallenge)
		}

		attempts := rapid.IntRange(1, 20).Draw(t, "attempts")
		admitted := 0
		var lastIssue *IssueResult
		var lastIssuedAt time.Time

		for i := 0; i < attempts; i++ {
			// Stay within the same fixed window so the 5-per-hour cap is
			// meaningfully exercised (crossing a window boundary legitimately
			// resets the count under fixed-window semantics — a different
			// property, covered by ratelimit's own Property 26).
			advance := rapid.IntRange(0, int((lim.Window-time.Second).Minutes())).Draw(t, "advanceMinutes")
			clock.now = windowStart.Add(time.Duration(advance) * time.Minute)

			result, err := svc.Issue(context.Background(), candidateID, ChallengeStytchEmailMatch)
			if err == nil {
				admitted++
				lastIssue = result
				lastIssuedAt = clock.now
			}
		}

		if admitted > lim.Max {
			t.Fatalf("candidate %q had %d Issue calls admitted in one window, want at most %d", candidateID, admitted, lim.Max)
		}

		if lastIssue != nil {
			if got := lastIssue.ExpiresAt.Sub(lastIssuedAt); got != 15*time.Minute {
				t.Fatalf("challenge ExpiresAt is %v after issuance, want exactly 15m", got)
			}

			// Once expired, Verify must always reject regardless of OTP correctness.
			challenge := &ChallengeState{
				Method:    ChallengeStytchEmailMatch,
				IssuedAt:  lastIssuedAt,
				ExpiresAt: lastIssue.ExpiresAt,
			}
			clock.now = lastIssue.ExpiresAt.Add(time.Second)
			otpVerified := rapid.Bool().Draw(t, "otpVerified")
			verifyResult, verifyErr := svc.Verify(context.Background(), challenge, otpVerified)
			if verifyErr == nil {
				t.Fatalf("Verify succeeded after expiry (otpVerified=%v), result=%+v", otpVerified, verifyResult)
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 25b: Verify never satisfies an unverified OTP
// Validates: Requirement 5.6
func TestProperty25_VerifyNeverSatisfiesWithoutOTPVerification(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		clock := &fakeClock{now: now}
		limiter := ratelimit.NewLimiter(nil, clock)
		svc := NewLinkService(limiter, clock)

		challenge := &ChallengeState{
			Method:    ChallengeStytchEmailMatch,
			IssuedAt:  now,
			ExpiresAt: now.Add(15 * time.Minute),
		}

		withinWindowMinutes := rapid.IntRange(0, 14).Draw(t, "withinWindowMinutes")
		clock.now = now.Add(time.Duration(withinWindowMinutes) * time.Minute)

		result, err := svc.Verify(context.Background(), challenge, false)
		if err == nil {
			t.Fatalf("Verify succeeded with otpVerified=false, result=%+v", result)
		}
	})
}
