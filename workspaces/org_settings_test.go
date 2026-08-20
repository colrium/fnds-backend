package workspaces

import (
	"context"
	"testing"

	"backend/stytch"
)

// Req 2.13 / D6 fix the email-provisioning settings for every Workspace, and
// design.md's test matrix calls for asserting the recorded create call carries
// them — but no such test existed, which is how "RESTRICTED" reached staging and
// failed every sign-up with 400 invalid_restricted_email_setting.
//
// RESTRICTED does not mean "locked down": it means JIT is permitted for domains
// on email_allowed_domains. With no such list Stytch rejects the create
// outright, and it refuses public domains like gmail.com in that list anyway, so
// RESTRICTED could never express "invitation only" — NOT_ALLOWED is the value
// that disables domain auto-join.
//
// Asserting on the arguments the adapter actually recorded, rather than on the
// constants, is the point: a call site that hardcodes a literal instead of the
// constant would still fail this.

func TestProvisioningSendsInvitationOnlyOrgSettings(t *testing.T) {
	t.Run("discovery create (live signup)", func(t *testing.T) {
		svc, fake := newProvisioningTestService(t)
		// Fail after the arguments are recorded so this stays DB-free.
		fake.FailAt["CreateOrganizationFromDiscovery"] = []stytch.FailSpec{
			{Attempt: 1, Kind: stytch.KindInvalidRequest, Status: 400},
		}

		_, _ = svc.CreateForOnboarding(context.Background(), CreateForOnboardingInput{
			OnboardingStateID:   "state-1",
			UserID:              "user-1",
			Email:               "employer@example.com",
			RawName:             "Acme Builders",
			IntermediateSession: "intermediate-token",
		})

		args, ok := recordedArgs[stytch.CreateOrgInput](t, fake, "CreateOrganizationFromDiscovery")
		if !ok {
			return
		}
		assertInvitationOnly(t, args.EmailJITProvisioning, args.EmailInvites)
	})

	t.Run("plain create (admin-provisioned migration)", func(t *testing.T) {
		svc, fake := newProvisioningTestService(t)
		fake.FailAt["CreateMember"] = []stytch.FailSpec{
			{Attempt: 1, Kind: stytch.KindInvalidRequest, Status: 400},
		}

		_, _ = svc.CreateForOnboarding(context.Background(), CreateForOnboardingInput{
			OnboardingStateID: "migration:user-1",
			UserID:            "user-1",
			Email:             "employer@example.com",
			RawName:           "Acme Builders",
			AdminProvisioned:  true,
		})

		args, ok := recordedArgs[stytch.CreateOrganizationInput](t, fake, "CreateOrganization")
		if !ok {
			return
		}
		assertInvitationOnly(t, args.EmailJITProvisioning, args.EmailInvites)
	})
}

// recordedArgs pulls the arguments of the first recorded call to op.
func recordedArgs[T any](t *testing.T, fake *stytch.Fake, op string) (T, bool) {
	t.Helper()
	var zero T
	for _, call := range fake.Calls {
		if call.Op != op {
			continue
		}
		args, ok := call.Args.(T)
		if !ok {
			t.Fatalf("%s args are %T, want %T", op, call.Args, zero)
		}
		return args, true
	}
	t.Errorf("%s was never called", op)
	return zero, false
}

func assertInvitationOnly(t *testing.T, jit, invites string) {
	t.Helper()
	if jit != "NOT_ALLOWED" {
		t.Errorf("email_jit_provisioning = %q, want %q — %q is rejected by Stytch without an allowed-domain list",
			jit, "NOT_ALLOWED", "RESTRICTED")
	}
	// Invitation-only membership is useless if a gmail invitee cannot be invited.
	if invites != "ALL_ALLOWED" {
		t.Errorf("email_invites = %q, want %q", invites, "ALL_ALLOWED")
	}
}
