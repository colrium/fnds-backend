package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/ratelimit"
	"backend/stytch"
	"backend/turnstile"
)

// fakeClock is a minimal Clock implementation for tests.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// newTestService returns a Service wired to a Stytch Fake and an in-memory rate limiter.
func newTestService(t *testing.T) (*Service, *stytch.Fake) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)}
	fake := stytch.NewFake(clock)
	adapter := stytch.NewWithClient(stytch.Config{
		ProjectID:     "proj-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
	}, fake)
	limiter := ratelimit.NewLimiter(nil, nil)
	svc := NewService(adapter, limiter)
	return svc, fake
}

func TestInitiateMagicLink_InvalidEmail(t *testing.T) {
	svc, fake := newTestService(t)

	// Ensure turnstile is disabled for test (no secret key set).
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	_, err := svc.InitiateMagicLink(context.Background(), InitiateInput{
		Email:          "not-an-email",
		TurnstileToken: "token",
		IP:             "127.0.0.1",
		RedirectURL:    "https://example.com/callback",
	})

	if err == nil {
		t.Fatal("expected error for invalid email")
	}

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Status != 400 {
		t.Errorf("expected status 400, got %d", svcErr.Status)
	}
	if svcErr.Field != "email" {
		t.Errorf("expected field 'email', got %q", svcErr.Field)
	}

	// Assert zero Stytch calls (Req 1.5).
	if fake.CallCount("DiscoveryEmailSend") != 0 {
		t.Errorf("expected zero Stytch calls, got %d", fake.CallCount("DiscoveryEmailSend"))
	}
}

func TestInitiateMagicLink_TurnstileRejected(t *testing.T) {
	svc, fake := newTestService(t)

	// Set a turnstile secret key so verification is active.
	t.Setenv("TURNSTILE_SECRET_KEY", "test-secret")

	// Point turnstile to a broken URL so it will fail (ErrUnavailable).
	origURL := turnstile.SiteVerifyURL
	turnstile.SiteVerifyURL = "http://invalid.test/turnstile-reject"
	defer func() { turnstile.SiteVerifyURL = origURL }()

	_, err := svc.InitiateMagicLink(context.Background(), InitiateInput{
		Email:          "user@example.com",
		TurnstileToken: "bad-token",
		IP:             "1.2.3.4",
		RedirectURL:    "https://example.com/callback",
	})

	if err == nil {
		t.Fatal("expected error for turnstile rejection")
	}

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Status != 400 {
		t.Errorf("expected status 400, got %d", svcErr.Status)
	}

	// Assert zero Stytch calls (Req 1.10).
	if fake.CallCount("DiscoveryEmailSend") != 0 {
		t.Errorf("expected zero Stytch calls on turnstile reject, got %d", fake.CallCount("DiscoveryEmailSend"))
	}
}

func TestInitiateMagicLink_Success(t *testing.T) {
	svc, fake := newTestService(t)

	// Disable turnstile for this test.
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	result, err := svc.InitiateMagicLink(context.Background(), InitiateInput{
		Email:          "  User@Example.COM  ",
		TurnstileToken: "token",
		IP:             "127.0.0.1",
		RedirectURL:    "https://example.com/callback",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the result is a fixed body (Req 25.4).
	if result.Stage != "verification_sent" {
		t.Errorf("expected stage 'verification_sent', got %q", result.Stage)
	}

	// Verify Stytch was called exactly once.
	if fake.CallCount("DiscoveryEmailSend") != 1 {
		t.Errorf("expected 1 Stytch call, got %d", fake.CallCount("DiscoveryEmailSend"))
	}

	// Verify the email was normalized when sent to Stytch.
	if len(fake.Calls) == 0 {
		t.Fatal("expected at least one recorded call")
	}
	call := fake.Calls[0]
	input, ok := call.Args.(stytch.DiscoveryEmailSendInput)
	if !ok {
		t.Fatalf("expected DiscoveryEmailSendInput, got %T", call.Args)
	}
	if input.Email != "user@example.com" {
		t.Errorf("expected normalized email 'user@example.com', got %q", input.Email)
	}
}

func TestInitiateMagicLink_EmailNormalization(t *testing.T) {
	svc, _ := newTestService(t)
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	// Various forms of the same email should all succeed.
	cases := []struct {
		input    string
		expected string
	}{
		{"USER@EXAMPLE.COM", "user@example.com"},
		{"  user@example.com  ", "user@example.com"},
		{"User@Example.Com", "user@example.com"},
	}

	for _, tc := range cases {
		_, err := svc.InitiateMagicLink(context.Background(), InitiateInput{
			Email:          tc.input,
			TurnstileToken: "token",
			IP:             "127.0.0.1",
			RedirectURL:    "https://example.com/callback",
		})
		if err != nil {
			t.Errorf("email %q: unexpected error: %v", tc.input, err)
		}
	}
}

func TestOAuthStartURL_Google(t *testing.T) {
	svc, fake := newTestService(t)

	url, err := svc.OAuthStartURL(context.Background(), "google", "https://example.com/oauth/callback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty URL")
	}

	// Verify Stytch was called.
	if fake.CallCount("OAuthDiscoveryStartURL") != 1 {
		t.Errorf("expected 1 Stytch call, got %d", fake.CallCount("OAuthDiscoveryStartURL"))
	}
}

func TestOAuthStartURL_UnsupportedProvider(t *testing.T) {
	svc, fake := newTestService(t)

	_, err := svc.OAuthStartURL(context.Background(), "facebook", "https://example.com/oauth/callback")
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Status != 400 {
		t.Errorf("expected status 400, got %d", svcErr.Status)
	}
	if svcErr.Code != "unsupported_provider" {
		t.Errorf("expected code 'unsupported_provider', got %q", svcErr.Code)
	}
	if svcErr.Field != "provider" {
		t.Errorf("expected field 'provider', got %q", svcErr.Field)
	}

	// Zero Stytch calls for unsupported provider.
	if fake.CallCount("OAuthDiscoveryStartURL") != 0 {
		t.Errorf("expected zero Stytch calls, got %d", fake.CallCount("OAuthDiscoveryStartURL"))
	}
}

func TestOAuthStartURL_EmptyProvider(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := svc.OAuthStartURL(context.Background(), "", "https://example.com/oauth/callback")
	if err == nil {
		t.Fatal("expected error for empty provider")
	}

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	if svcErr.Status != 400 {
		t.Errorf("expected status 400, got %d", svcErr.Status)
	}
}
