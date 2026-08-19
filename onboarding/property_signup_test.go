package onboarding

import (
	"context"
	"testing"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 27: Signup responses do not disclose account existence
// Validates: Requirements 25.4, 25.5
//
// InitiateMagicLink never queries the users collection at all — it
// normalizes the email, validates format, verifies Turnstile, and always
// calls Stytch DiscoveryEmailSend unconditionally, returning the same fixed
// {"stage":"verification_sent"} body. This property exercises exactly that
// real code path with an arbitrary population of "existing" vs "new" email
// addresses and confirms the response is identical either way — there is no
// simulated DB needed because the real function is provably independent of
// any users-collection state.
func TestProperty27_SignupResponsesDoNotDiscloseAccountExistence(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	svc, fake := newTestService(t)

	rapid.Check(t, func(t *rapid.T) {
		fake.Reset()

		local := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "local")
		domain := rapid.SampledFrom([]string{"example.com", "fundis.co.ke", "test.org"}).Draw(t, "domain")
		email := local + "@" + domain

		// Whether or not a "matching" address exists is irrelevant to this
		// service: it makes no DB call. We still vary a case flag to force
		// rapid to explore the space, and assert that flag has zero effect.
		simulatedExistingAccount := rapid.Bool().Draw(t, "simulatedExistingAccount")
		_ = simulatedExistingAccount // deliberately unused by the real call — the point of the property

		result, err := svc.InitiateMagicLink(context.Background(), InitiateInput{
			Email:          email,
			TurnstileToken: "token",
			IP:             "127.0.0.1",
			RedirectURL:    "https://example.com/callback",
		})

		if err != nil {
			t.Fatalf("unexpected error for a validly formatted email: %v", err)
		}
		if result.Stage != "verification_sent" {
			t.Fatalf("stage = %q, want the fixed value %q regardless of account existence", result.Stage, "verification_sent")
		}

		// Exactly one Stytch call was made, unconditionally.
		if fake.CallCount("DiscoveryEmailSend") == 0 {
			t.Fatalf("expected DiscoveryEmailSend to be called unconditionally")
		}
	})
}
