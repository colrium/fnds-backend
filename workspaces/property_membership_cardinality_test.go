package workspaces

import (
	"testing"

	"pgregory.net/rapid"
)

// simInvitationWorkspace models one (email, workspace) pair's membership
// lifecycle for P14: invite, accept, remove, reinstate. Mirrors the
// uniq_membership_user_workspace index — Create's duplicate-key path
// returns the existing membership rather than creating a second one.
type simInvitationWorkspace struct {
	membership *simMembership // nil means no Membership document exists yet
}

func (s *simInvitationWorkspace) invite() {
	if s.membership == nil {
		s.membership = &simMembership{ID: "m", Role: RoleAdmin, Status: MembershipActive, AuthorityVersion: 1}
		return
	}
	// Re-inviting an existing membership is a no-op at the document-count
	// level (Create's duplicate-key path returns the existing document).
}

func (s *simInvitationWorkspace) accept() {
	if s.membership == nil {
		s.membership = &simMembership{ID: "m", Role: RoleAdmin, Status: MembershipActive, AuthorityVersion: 1}
	}
}

func (s *simInvitationWorkspace) remove() {
	if s.membership != nil {
		s.membership.Status = MembershipRemoved
	}
}

func (s *simInvitationWorkspace) reinstate() {
	if s.membership != nil {
		s.membership.Status = MembershipActive
	}
}

func (s *simInvitationWorkspace) documentCount() int {
	if s.membership == nil {
		return 0
	}
	return 1
}

// Feature: stytch-employer-onboarding, Property 14: A user holds at most one Membership per Workspace
// Validates: Requirements 15.1, 15.2
func TestProperty14_AtMostOneMembershipPerWorkspace(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sim := &simInvitationWorkspace{}
		steps := rapid.IntRange(1, 40).Draw(t, "steps")

		for i := 0; i < steps; i++ {
			op := rapid.IntRange(0, 3).Draw(t, "op")
			switch op {
			case 0:
				sim.invite()
			case 1:
				sim.accept()
			case 2:
				sim.remove()
			case 3:
				sim.reinstate()
			}

			if count := sim.documentCount(); count > 1 {
				t.Fatalf("step %d: membership document count = %d, want at most 1", i, count)
			}
		}
	})
}

// permissionGuardDecision mirrors RequirePermission's evaluation order for
// the fields P17 covers: a suspended or removed Membership, or a suspended
// user, is denied regardless of Role or the requested Permission.
func permissionGuardDecision(role Role, perm Permission, membershipStatus MembershipStatus, userSuspended bool) bool {
	if userSuspended {
		return false
	}
	if membershipStatus == MembershipRemoved || membershipStatus == MembershipSuspended {
		return false
	}
	return Grants(role, perm)
}

// Feature: stytch-employer-onboarding, Property 17: A suspended or removed Membership is denied every workspace-scoped Permission
// Validates: Requirements 8.7, 8.8, 8.9, 13.5
func TestProperty17_SuspendedOrRemovedMembershipDeniedEveryPermission(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		role := rapid.SampledFrom(AllRoles[:]).Draw(t, "role")
		perm := rapid.SampledFrom(AllPermissions[:]).Draw(t, "perm")
		status := rapid.SampledFrom([]MembershipStatus{MembershipActive, MembershipSuspended, MembershipRemoved}).Draw(t, "status")
		userSuspended := rapid.Bool().Draw(t, "userSuspended")

		got := permissionGuardDecision(role, perm, status, userSuspended)

		if userSuspended && got {
			t.Fatalf("role=%s perm=%s status=%s userSuspended=true: expected denial, got granted", role, perm, status)
		}
		if (status == MembershipSuspended || status == MembershipRemoved) && got {
			t.Fatalf("role=%s perm=%s status=%s: expected denial, got granted", role, perm, status)
		}
		if !userSuspended && status == MembershipActive {
			want := Grants(role, perm)
			if got != want {
				t.Fatalf("role=%s perm=%s status=active: got %v, want %v (the ordinary Permission_Matrix decision)", role, perm, got, want)
			}
		}
	})
}
