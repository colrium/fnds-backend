package identity

import (
	"context"
	"errors"
	"time"

	"backend/ratelimit"
	"backend/stytch"
)

// ChallengeMethod identifies how the link challenge is verified.
type ChallengeMethod string

const (
	ChallengeStytchEmailMatch ChallengeMethod = "stytch_email_match"
	ChallengePhoneOTP         ChallengeMethod = "phone_otp"
)

// ChallengeState holds the in-flight challenge state stored on the onboarding record.
type ChallengeState struct {
	Method    ChallengeMethod
	IssuedAt  time.Time
	ExpiresAt time.Time // issued_at + 15m (Req 5.4)
	Attempts  int
}

// LinkService handles the account linking flow (Req 5).
type LinkService struct {
	rate  ratelimit.RateLimiter
	clock stytch.Clock
}

// NewLinkService constructs a LinkService.
func NewLinkService(rate ratelimit.RateLimiter, clock stytch.Clock) *LinkService {
	return &LinkService{rate: rate, clock: clock}
}

// SelectChallenge is pure: determines which challenge method to use based on
// whether the candidate's email matches the Stytch-verified email and whether
// the candidate has a phone number.
//
// - Matching normalized emails -> stytch_email_match (satisfied immediately, Req 5.2)
// - Candidate has phone, emails differ -> phone_otp (Req 5.3)
// - No phone, emails differ -> error (contact support)
func SelectChallenge(candidateEmail, candidatePhone, verifiedEmail string) (ChallengeMethod, error) {
	normCandidate := NormalizeEmail(candidateEmail)
	normVerified := NormalizeEmail(verifiedEmail)

	if normCandidate == normVerified {
		return ChallengeStytchEmailMatch, nil
	}

	if candidatePhone != "" {
		return ChallengePhoneOTP, nil
	}

	return "", errors.New("candidate has no phone and emails differ; contact support")
}

// IssueResult is returned by Issue when a challenge is created.
type IssueResult struct {
	Method    ChallengeMethod
	ExpiresAt time.Time
}

// Issue creates a new link challenge. For stytch_email_match, the challenge is
// satisfied immediately. For phone_otp, a one-time code must be sent (handled
// by the caller using users.IssuePhoneOTP) and verified later.
//
// The 5-per-hour cap runs through the Rate_Limiter (Req 5.5).
func (l *LinkService) Issue(ctx context.Context, candidateID string, method ChallengeMethod) (*IssueResult, error) {
	// Check rate limit (5 per hour per candidate).
	decision := l.rate.Allow(ctx, ratelimit.LimitLinkChallenge, candidateID)
	if !decision.Allowed {
		return nil, &LinkError{
			Status:  429,
			Code:    "link_challenge_rate_limited",
			Message: "too many link challenge attempts",
		}
	}

	now := l.clock.Now()
	result := &IssueResult{
		Method:    method,
		ExpiresAt: now.Add(15 * time.Minute), // Req 5.4
	}

	return result, nil
}

// VerifyResult indicates whether the challenge was satisfied.
type VerifyResult struct {
	Satisfied bool
}

// Verify checks a submitted OTP code against the challenge.
// Returns 401 and increments attempt count on incorrect value (Req 5.6).
// The OTP's own verification is handled by the caller (users.VerifyOTP).
//
// This method checks:
// - Challenge not expired (Req 5.4)
// - Attempt count not exceeded (max 5 per hour via rate limiter)
func (l *LinkService) Verify(ctx context.Context, challenge *ChallengeState, otpVerified bool) (*VerifyResult, error) {
	now := l.clock.Now()

	// Check if challenge has expired (Req 5.4).
	if now.After(challenge.ExpiresAt) {
		return nil, &LinkError{
			Status:  401,
			Code:    "challenge_expired",
			Message: "link challenge has expired",
		}
	}

	// If OTP verification failed (Req 5.6).
	if !otpVerified {
		return nil, &LinkError{
			Status:  401,
			Code:    "challenge_incorrect",
			Message: "incorrect verification code",
		}
	}

	return &VerifyResult{Satisfied: true}, nil
}

// LinkError represents a domain error in the linking flow.
type LinkError struct {
	Status  int
	Code    string
	Message string
}

func (e *LinkError) Error() string { return e.Message }
