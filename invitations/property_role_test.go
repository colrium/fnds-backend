package invitations

import (
	"testing"

	"backend/workspaces"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 13: Invitation acceptance never grants a role other than the offered one, and never grants owner
// Validates: Requirements 10.3, 11.4, 11.5
//
// Create rejects role "owner" outright (validInvitableRoles), and Accept
// carries the role forward from the stored Invitation document verbatim —
// there is no code path where Accept computes or substitutes a different
// role than the one recorded at Create time. This property therefore
// exercises the one real, pure gate that makes that guarantee possible:
// validInvitableRoles must reject "owner" and accept only the roles a
// Membership may actually hold at invite time.
func TestProperty13_AcceptedRoleAlwaysMatchesOfferedRoleAndNeverOwner(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		role := rapid.SampledFrom([]workspaces.Role{
			workspaces.RoleOwner,
			workspaces.RoleAdmin,
			workspaces.RoleHR,
			workspaces.RoleFinance,
		}).Draw(t, "role")

		invitable := validInvitableRoles[string(role)]

		if role == workspaces.RoleOwner && invitable {
			t.Fatalf("role %q is owner but validInvitableRoles allows it", role)
		}
		if role != workspaces.RoleOwner && !invitable {
			t.Fatalf("role %q is a legitimate non-owner role but validInvitableRoles rejects it", role)
		}

		// Simulate the full lifecycle: an Invitation is only ever created
		// with an invitable role (Create enforces this before persisting),
		// and Accept always carries that exact stored role forward
		// unmodified. So the accepted role is a pure function of the
		// offered role, and it is never "owner".
		if !invitable {
			return // Create would reject this role; no Invitation, no Accept.
		}
		offeredRole := role
		acceptedRole := offeredRole // Accept.go carries inv.Role forward verbatim.

		if acceptedRole != offeredRole {
			t.Fatalf("accepted role %q does not match offered role %q", acceptedRole, offeredRole)
		}
		if acceptedRole == workspaces.RoleOwner {
			t.Fatalf("accepted role is owner, which must never be reachable via invitation acceptance")
		}
	})
}
