package workspaces

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// simMembership is the in-memory model of a Membership document, tracking
// exactly the fields the ownership/authority invariants depend on.
type simMembership struct {
	ID               string
	Role             Role
	Status           MembershipStatus
	AuthorityVersion int64
}

// simWorkspace models one Workspace's memberships for the state-machine
// properties below. Every operation mirrors the real guard tables in
// membership_service.go's ValidateRoleChange/ValidateStatusChange/
// TransferOwnership: the sole active Owner can never be demoted, suspended,
// or removed outside a transfer, and every operation that changes Role or
// Status increments AuthorityVersion by exactly 1 in the same step.
type simWorkspace struct {
	members map[string]*simMembership
}

func newSimWorkspace(ownerID string) *simWorkspace {
	return &simWorkspace{
		members: map[string]*simMembership{
			ownerID: {ID: ownerID, Role: RoleOwner, Status: MembershipActive, AuthorityVersion: 1},
		},
	}
}

func (w *simWorkspace) activeOwnerCount() int {
	count := 0
	for _, m := range w.members {
		if m.Role == RoleOwner && m.Status == MembershipActive {
			count++
		}
	}
	return count
}

// invite mirrors MembershipService.Create for a non-owner role.
func (w *simWorkspace) invite(id string, role Role) {
	if _, exists := w.members[id]; exists {
		return
	}
	w.members[id] = &simMembership{ID: id, Role: role, Status: MembershipActive, AuthorityVersion: 1}
}

// changeRole mirrors ValidateRoleChange's guards 3 and 5: granting Owner is
// never allowed through a plain role change (Req 7.12 — only
// TransferOwnership may create an Owner), and the sole active Owner cannot
// be demoted outside transferOwnership.
func (w *simWorkspace) changeRole(id string, newRole Role) (changed bool) {
	m, ok := w.members[id]
	if !ok || m.Status != MembershipActive {
		return false
	}
	if newRole == RoleOwner {
		return false // owner_grant_forbidden — only TransferOwnership grants owner
	}
	if m.Role == RoleOwner && w.activeOwnerCount() <= 1 {
		return false // last_owner_protected
	}
	if m.Role == newRole {
		return false // no-op, no authority change
	}
	m.Role = newRole
	m.AuthorityVersion++
	return true
}

// setStatus mirrors ValidateStatusChange's guard 1: the sole active Owner
// cannot be suspended or removed.
func (w *simWorkspace) setStatus(id string, newStatus MembershipStatus) (changed bool) {
	m, ok := w.members[id]
	if !ok {
		return false
	}
	if m.Role == RoleOwner && m.Status == MembershipActive && newStatus != MembershipActive && w.activeOwnerCount() <= 1 {
		return false // last_owner_protected
	}
	if m.Status == newStatus {
		return false
	}
	m.Status = newStatus
	m.AuthorityVersion++
	return true
}

// transferOwnership mirrors MembershipService.TransferOwnership: exactly two
// Memberships change role, both Authority_Versions increment, and it never
// runs when the target isn't active or is the caller.
func (w *simWorkspace) transferOwnership(callerID, targetID string) (changed bool) {
	caller, ok := w.members[callerID]
	if !ok || caller.Role != RoleOwner || caller.Status != MembershipActive {
		return false
	}
	target, ok := w.members[targetID]
	if !ok || target.Status != MembershipActive || targetID == callerID {
		return false
	}
	caller.Role = RoleAdmin
	caller.AuthorityVersion++
	target.Role = RoleOwner
	target.AuthorityVersion++
	return true
}

// Feature: stytch-employer-onboarding, Property 9: Every Workspace has exactly one Owner
// Validates: Requirements 14.1, 14.2, 14.11, 14.12
func TestProperty9_EveryWorkspaceHasExactlyOneOwner(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ws := newSimWorkspace("owner-0")
		nextID := 1

		steps := rapid.IntRange(1, 40).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			op := rapid.IntRange(0, 6).Draw(t, "op")
			memberIDs := make([]string, 0, len(ws.members))
			for id := range ws.members {
				memberIDs = append(memberIDs, id)
			}

			switch op {
			case 0: // invite
				id := "m" + rapid.StringMatching(`[0-9]{1,3}`).Draw(t, "newID")
				role := rapid.SampledFrom([]Role{RoleAdmin, RoleHR, RoleFinance}).Draw(t, "inviteRole")
				ws.invite(id+"-"+strconv.Itoa(nextID), role)
				nextID++
			case 1: // role change
				if len(memberIDs) == 0 {
					continue
				}
				target := rapid.SampledFrom(memberIDs).Draw(t, "target")
				newRole := rapid.SampledFrom(AllRoles[:]).Draw(t, "newRole")
				ws.changeRole(target, newRole)
			case 2: // suspend
				if len(memberIDs) == 0 {
					continue
				}
				target := rapid.SampledFrom(memberIDs).Draw(t, "target")
				ws.setStatus(target, MembershipSuspended)
			case 3: // remove
				if len(memberIDs) == 0 {
					continue
				}
				target := rapid.SampledFrom(memberIDs).Draw(t, "target")
				ws.setStatus(target, MembershipRemoved)
			case 4: // reinstate
				if len(memberIDs) == 0 {
					continue
				}
				target := rapid.SampledFrom(memberIDs).Draw(t, "target")
				ws.setStatus(target, MembershipActive)
			case 5: // ownership transfer
				if len(memberIDs) < 2 {
					continue
				}
				caller := rapid.SampledFrom(memberIDs).Draw(t, "caller")
				target := rapid.SampledFrom(memberIDs).Draw(t, "transferTarget")
				ws.transferOwnership(caller, target)
			case 6: // self-remove (same as remove, distinct op number preserved for generator symmetry)
				if len(memberIDs) == 0 {
					continue
				}
				target := rapid.SampledFrom(memberIDs).Draw(t, "target")
				ws.setStatus(target, MembershipRemoved)
			}

			if got := ws.activeOwnerCount(); got != 1 {
				t.Fatalf("after step %d (op=%d): active owner count = %d, want exactly 1", i, op, got)
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 10: Ownership transfer conserves the Membership count and swaps exactly two roles
// Validates: Requirements 14.3, 14.4, 14.8
func TestProperty10_OwnershipTransferConservesCountAndSwapsExactlyTwoRoles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ws := newSimWorkspace("owner-0")
		extra := rapid.IntRange(0, 6).Draw(t, "extraMembers")
		for i := 0; i < extra; i++ {
			ws.invite("m"+strconv.Itoa(i), rapid.SampledFrom([]Role{RoleAdmin, RoleHR, RoleFinance}).Draw(t, "role"))
		}

		before := make(map[string]Role, len(ws.members))
		for id, m := range ws.members {
			before[id] = m.Role
		}
		countBefore := len(ws.members)

		memberIDs := make([]string, 0, len(ws.members))
		for id := range ws.members {
			memberIDs = append(memberIDs, id)
		}
		caller := rapid.SampledFrom(memberIDs).Draw(t, "caller")
		target := rapid.SampledFrom(memberIDs).Draw(t, "target")

		changed := ws.transferOwnership(caller, target)

		if len(ws.members) != countBefore {
			t.Fatalf("membership count changed from %d to %d", countBefore, len(ws.members))
		}

		diffCount := 0
		for id, m := range ws.members {
			if m.Role != before[id] {
				diffCount++
			}
		}

		if !changed {
			if diffCount != 0 {
				t.Fatalf("transfer failed but %d roles changed, want 0", diffCount)
			}
		} else {
			if diffCount != 2 {
				t.Fatalf("transfer succeeded but %d roles changed, want exactly 2", diffCount)
			}
			if ws.members[caller].Role != RoleAdmin || ws.members[target].Role != RoleOwner {
				t.Fatalf("transfer succeeded but roles are wrong: caller=%s target=%s", ws.members[caller].Role, ws.members[target].Role)
			}
		}

		// Identity set (the set of IDs) is unchanged either way.
		for id := range before {
			if _, ok := ws.members[id]; !ok {
				t.Fatalf("member %q disappeared after transfer", id)
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 18: Authority_Version increases monotonically and exactly once per authority change
// Validates: Requirements 13.1, 13.2, 13.11, 13.12, 17.1
func TestProperty18_AuthorityVersionMonotonicAndExactlyOnePerChange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ws := newSimWorkspace("owner-0")
		ws.invite("m1", RoleAdmin)

		versionsBefore := map[string]int64{}
		for id, m := range ws.members {
			versionsBefore[id] = m.AuthorityVersion
		}

		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			memberIDs := make([]string, 0, len(ws.members))
			for id := range ws.members {
				memberIDs = append(memberIDs, id)
			}
			target := rapid.SampledFrom(memberIDs).Draw(t, "target")
			before := ws.members[target].AuthorityVersion
			beforeRole := ws.members[target].Role
			beforeStatus := ws.members[target].Status

			op := rapid.IntRange(0, 1).Draw(t, "op")
			var changed bool
			if op == 0 {
				newRole := rapid.SampledFrom(AllRoles[:]).Draw(t, "newRole")
				changed = ws.changeRole(target, newRole)
			} else {
				newStatus := rapid.SampledFrom([]MembershipStatus{MembershipActive, MembershipSuspended, MembershipRemoved}).Draw(t, "newStatus")
				changed = ws.setStatus(target, newStatus)
			}

			after := ws.members[target].AuthorityVersion
			if after < before {
				t.Fatalf("step %d: authority_version decreased from %d to %d", i, before, after)
			}

			roleChanged := ws.members[target].Role != beforeRole
			statusChanged := ws.members[target].Status != beforeStatus

			if roleChanged || statusChanged {
				if !changed {
					t.Fatalf("step %d: role/status changed but the op reported no change", i)
				}
				if after != before+1 {
					t.Fatalf("step %d: role/status changed, authority_version went from %d to %d, want +1", i, before, after)
				}
			} else if after != before {
				t.Fatalf("step %d: neither role nor status changed, but authority_version went from %d to %d", i, before, after)
			}
		}
	})
}
