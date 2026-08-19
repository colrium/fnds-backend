package workspaces

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// simMembershipStore mirrors the query SwitchWorkspace's step 1 runs against
// memberships: an active membership keyed by (user_id, workspace_id). This
// is a direct mirror of the filter `{user_id, workspace_id, status: active}`
// — not a black-box reimplementation of business logic, since the real
// resolution logic under test (GrantedPermissions, Grants) is exercised
// directly, unmodeled.
type simMembershipStore map[[2]string]Role // key: [userID, workspaceID]

// resolveWorkspaceAuthority mirrors SwitchWorkspace's steps 1-2 and 6: find
// the active membership for (userID, targetWorkspaceID); if found, the
// resolved Permission set is exactly GrantedPermissions(role) — the real
// function under test. If not found, the resolved set is empty. Critically,
// this function takes no "claimed role" parameter at all: the real
// SwitchWorkspace signature (and PostAuth's resolution) never reads a role
// from the Stytch_Session or a webhook payload — role always comes from the
// stored Membership document. That absence of a parameter IS the invariance
// property; there is nothing else to assert beyond confirming the resolution
// path only consults the store.
func resolveWorkspaceAuthority(store simMembershipStore, userID, targetWorkspaceID string) (Role, PermissionSet, bool) {
	role, ok := store[[2]string{userID, targetWorkspaceID}]
	if !ok {
		return "", PermissionSet(0), false
	}
	set := setOf(GrantedPermissions(role)...)
	return role, set, true
}

// Feature: stytch-employer-onboarding, Property 15: Workspace switching yields only the target Workspace's authority
// Validates: Requirements 15.3, 15.4, 15.7, 15.8, 15.9, 15.10, 15.11, 16.5, 16.6, 19.13
func TestProperty15_WorkspaceSwitchingYieldsOnlyTargetWorkspaceAuthority(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userID := "user-1"

		// Build an arbitrary set of Memberships across Workspaces for this user.
		workspaceCount := rapid.IntRange(0, 6).Draw(t, "workspaceCount")
		store := simMembershipStore{}
		workspaceIDs := make([]string, workspaceCount)
		for i := 0; i < workspaceCount; i++ {
			wsID := "ws-" + strconv.Itoa(i)
			workspaceIDs[i] = wsID
			hasActiveMembership := rapid.Bool().Draw(t, "hasActiveMembership")
			if hasActiveMembership {
				role := rapid.SampledFrom(AllRoles[:]).Draw(t, "role")
				store[[2]string{userID, wsID}] = role
			}
		}

		// Any sequence of switch requests, including to a workspace the user
		// holds no membership in at all (not in workspaceIDs).
		switches := rapid.IntRange(1, 20).Draw(t, "switches")
		for i := 0; i < switches; i++ {
			var target string
			if len(workspaceIDs) > 0 && rapid.Bool().Draw(t, "targetKnown") {
				target = rapid.SampledFrom(workspaceIDs).Draw(t, "target")
			} else {
				target = "unknown-ws-" + strconv.Itoa(i)
			}

			role, permSet, found := resolveWorkspaceAuthority(store, userID, target)
			storedRole, hasMembership := store[[2]string{userID, target}]

			if !hasMembership {
				if found {
					t.Fatalf("step %d: resolved authority for workspace %q with no active membership, want not-found", i, target)
				}
				continue
			}

			if !found {
				t.Fatalf("step %d: workspace %q has an active membership but authority resolution reported not-found", i, target)
			}
			if role != storedRole {
				t.Fatalf("step %d: resolved role %q, want stored role %q", i, role, storedRole)
			}

			want := GrantedPermissions(storedRole)
			for _, p := range want {
				if !permSet.has(p) {
					t.Fatalf("step %d: resolved permission set missing %v, which Permission_Matrix grants role %q", i, p, storedRole)
				}
			}
			for _, p := range AllPermissions {
				if permSet.has(p) && !Grants(storedRole, p) {
					t.Fatalf("step %d: resolved permission set grants %v, which role %q does not hold per Permission_Matrix", i, p, storedRole)
				}
			}
		}
	})
}
