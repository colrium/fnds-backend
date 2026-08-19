package identity

import (
	"context"
	"testing"
	"time"

	"backend/ratelimit"
)

// fakeClock is a controllable clock for testing.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func TestSelectChallenge_EmailsMatch(t *testing.T) {
	method, err := SelectChallenge("user@example.com", "+254700000000", "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != ChallengeStytchEmailMatch {
		t.Errorf("expected %q, got %q", ChallengeStytchEmailMatch, method)
	}
}

func TestSelectChallenge_EmailsDifferWithPhone(t *testing.T) {
	method, err := SelectChallenge("old@example.com", "+254700000000", "new@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != ChallengePhoneOTP {
		t.Errorf("expected %q, got %q", ChallengePhoneOTP, method)
	}
}

func TestSelectChallenge_EmailsDifferNoPhone(t *testing.T) {
	_, err := SelectChallenge("old@example.com", "", "new@example.com")
	if err == nil {
		t.Fatal("expected an error when emails differ and no phone, got nil")
	}
}

func TestSelectChallenge_CaseInsensitive(t *testing.T) {
	method, err := SelectChallenge("User@Example.COM", "+254700000000", "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != ChallengeStytchEmailMatch {
		t.Errorf("expected %q (case-insensitive match), got %q", ChallengeStytchEmailMatch, method)
	}
}

func TestIssue_Success(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	limiter := ratelimit.NewLimiter(nil, clock)
	svc := NewLinkService(limiter, clock)

	result, err := svc.Issue(context.Background(), "candidate-123", ChallengePhoneOTP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Method != ChallengePhoneOTP {
		t.Errorf("expected method %q, got %q", ChallengePhoneOTP, result.Method)
	}
	expectedExpiry := now.Add(15 * time.Minute)
	if !result.ExpiresAt.Equal(expectedExpiry) {
		t.Errorf("expected expiry %v, got %v", expectedExpiry, result.ExpiresAt)
	}
}

func TestIssue_RateLimited(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	limiter := ratelimit.NewLimiter(nil, clock)
	svc := NewLinkService(limiter, clock)

	candidateID := "candidate-rate-limit"

	// Exhaust the 5-per-hour limit.
	for i := 0; i < 5; i++ {
		_, err := svc.Issue(context.Background(), candidateID, ChallengePhoneOTP)
		if err != nil {
			t.Fatalf("issue %d: unexpected error: %v", i+1, err)
		}
	}

	// The 6th attempt should be rate-limited.
	_, err := svc.Issue(context.Background(), candidateID, ChallengePhoneOTP)
	if err == nil {
		t.Fatal("expected rate limit error on 6th attempt, got nil")
	}
	var linkErr *LinkError
	if !isLinkError(err, &linkErr) {
		t.Fatalf("expected *LinkError, got %T: %v", err, err)
	}
	if linkErr.Status != 429 {
		t.Errorf("expected status 429, got %d", linkErr.Status)
	}
	if linkErr.Code != "link_challenge_rate_limited" {
		t.Errorf("expected code %q, got %q", "link_challenge_rate_limited", linkErr.Code)
	}
}

func TestVerify_Expired(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	limiter := ratelimit.NewLimiter(nil, clock)
	svc := NewLinkService(limiter, clock)

	challenge := &ChallengeState{
		Method:    ChallengePhoneOTP,
		IssuedAt:  time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2025, 1, 15, 10, 15, 0, 0, time.UTC), // expired 15 min ago
		Attempts:  0,
	}

	_, err := svc.Verify(context.Background(), challenge, true)
	if err == nil {
		t.Fatal("expected error for expired challenge, got nil")
	}
	var linkErr *LinkError
	if !isLinkError(err, &linkErr) {
		t.Fatalf("expected *LinkError, got %T: %v", err, err)
	}
	if linkErr.Status != 401 {
		t.Errorf("expected status 401, got %d", linkErr.Status)
	}
	if linkErr.Code != "challenge_expired" {
		t.Errorf("expected code %q, got %q", "challenge_expired", linkErr.Code)
	}
}

func TestVerify_Incorrect(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	limiter := ratelimit.NewLimiter(nil, clock)
	svc := NewLinkService(limiter, clock)

	challenge := &ChallengeState{
		Method:    ChallengePhoneOTP,
		IssuedAt:  time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2025, 1, 15, 10, 15, 0, 0, time.UTC),
		Attempts:  0,
	}

	_, err := svc.Verify(context.Background(), challenge, false)
	if err == nil {
		t.Fatal("expected error for incorrect OTP, got nil")
	}
	var linkErr *LinkError
	if !isLinkError(err, &linkErr) {
		t.Fatalf("expected *LinkError, got %T: %v", err, err)
	}
	if linkErr.Status != 401 {
		t.Errorf("expected status 401, got %d", linkErr.Status)
	}
	if linkErr.Code != "challenge_incorrect" {
		t.Errorf("expected code %q, got %q", "challenge_incorrect", linkErr.Code)
	}
}

func TestVerify_Success(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	limiter := ratelimit.NewLimiter(nil, clock)
	svc := NewLinkService(limiter, clock)

	challenge := &ChallengeState{
		Method:    ChallengePhoneOTP,
		IssuedAt:  time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2025, 1, 15, 10, 15, 0, 0, time.UTC),
		Attempts:  0,
	}

	result, err := svc.Verify(context.Background(), challenge, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Satisfied {
		t.Error("expected Satisfied to be true")
	}
}

// isLinkError is a helper that unwraps err into a *LinkError.
func isLinkError(err error, target **LinkError) bool {
	le, ok := err.(*LinkError)
	if ok {
		*target = le
		return true
	}
	return false
}
