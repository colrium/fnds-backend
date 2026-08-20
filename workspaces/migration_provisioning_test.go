package workspaces

import (
	"context"
	"testing"
	"time"

	"backend/audit"
	"backend/stytch"
)

// The bulk migration (Req 21.8) previously reached Stytch's *discovery*
// Organization-create with an empty intermediate session token, because
// CreateForOnboarding took that branch whenever StytchOrgID was empty and the
// job supplies neither value. Real Stytch rejects that call, so every
// candidate would have been recorded "failed" while the Fake — which ignores
// the token entirely — kept the tests green.
//
// These tests assert on WHICH Stytch operation is invoked, which is the part
// the Fake cannot paper over. Each forces the last Stytch call to fail so
// CreateForOnboarding returns before touching Mongo (every Stytch call in it
// precedes the first write), keeping these tests DB-free.

func newProvisioningTestService(t *testing.T) (*WorkspaceService, *stytch.Fake) {
	t.Helper()
	clock := stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fake := stytch.NewFake(clock)
	cfg := stytch.Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   1,
		Clock:         clock,
	}
	adapter := stytch.NewWithClient(cfg, fake)
	return NewWorkspaceService(adapter, audit.NewLogger(), clock, nil), fake
}

func TestAdminProvisionedUsesPlainOrgCreateNotDiscovery(t *testing.T) {
	svc, fake := newProvisioningTestService(t)

	// Fail the Member-create so we return before the workspace insert. The
	// Organization-create has already happened by then, so the call record
	// still proves which branch ran.
	fake.FailAt["CreateMember"] = []stytch.FailSpec{
		{Attempt: 1, Kind: stytch.KindInvalidRequest, Status: 400},
	}

	_, err := svc.CreateForOnboarding(context.Background(), CreateForOnboardingInput{
		OnboardingStateID: "migration:user-1",
		UserID:            "user-1",
		Email:             "employer@example.com",
		RawName:           "Acme Builders",
		AdminProvisioned:  true,
	})
	if err == nil {
		t.Fatal("expected the injected CreateMember failure to surface")
	}

	// The regression: this call must never happen without an intermediate
	// session token, because real Stytch rejects it.
	if n := fake.CallCount("CreateOrganizationFromDiscovery"); n != 0 {
		t.Errorf("CreateOrganizationFromDiscovery called %d times, want 0", n)
	}
	if n := fake.CallCount("CreateOrganization"); n != 1 {
		t.Errorf("CreateOrganization called %d times, want 1", n)
	}
	if n := fake.CallCount("CreateMember"); n != 1 {
		t.Errorf("CreateMember called %d times, want 1", n)
	}
	// Nobody is signing in, so no session may be exchanged either.
	if n := fake.CallCount("ExchangeSession"); n != 0 {
		t.Errorf("ExchangeSession called %d times, want 0", n)
	}
}

func TestLiveSignupStillUsesDiscoveryOrgCreate(t *testing.T) {
	svc, fake := newProvisioningTestService(t)

	fake.FailAt["CreateOrganizationFromDiscovery"] = []stytch.FailSpec{
		{Attempt: 1, Kind: stytch.KindInvalidRequest, Status: 400},
	}

	_, err := svc.CreateForOnboarding(context.Background(), CreateForOnboardingInput{
		OnboardingStateID:   "state-1",
		UserID:              "user-1",
		Email:               "employer@example.com",
		RawName:             "Acme Builders",
		IntermediateSession: "intermediate-token",
		// AdminProvisioned deliberately false — the Employer is present.
	})
	if err == nil {
		t.Fatal("expected the injected failure to surface")
	}

	if n := fake.CallCount("CreateOrganizationFromDiscovery"); n != 1 {
		t.Errorf("CreateOrganizationFromDiscovery called %d times, want 1", n)
	}
	if n := fake.CallCount("CreateOrganization"); n != 0 {
		t.Errorf("plain CreateOrganization called %d times on the live path, want 0", n)
	}
}

// The retained-IDs re-run path (both StytchOrgID and StytchMemberID already
// known) makes zero Stytch calls, so nothing fails early and execution reaches
// the workspace insert — it cannot be covered without a live Mongo, and is left
// to the integration suite.
