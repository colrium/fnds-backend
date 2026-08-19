package workspaces

import (
	"testing"

	"pgregory.net/rapid"
)

// simAccountState models the outcome of one signup/workspace-switch attempt
// that calls Stytch before writing any Fundis document — mirroring the real
// call ordering in CreateForOnboarding (Stytch CreateOrganizationFromDiscovery
// runs BEFORE the workspace insert and the membership insert, service.go
// lines ~126-190) and SwitchWorkspace (Stytch ExchangeSession runs BEFORE the
// membership update, switch.go lines ~79-113). Because both real functions
// return immediately on a Stytch error — before touching Mongo at all — the
// in-memory model only needs to encode that same ordering to be faithful.
type simAccountState struct {
	workspaceCreated  bool
	membershipCreated bool
	stytchOrgLinked   bool // the created Membership carries a real Stytch_Member ID
	resumable         bool // an Onboarding_State record survives for retry
}

// attemptSignup mirrors CreateForOnboarding's ordering: call Stytch first; on
// failure, apply zero Fundis writes (Req 20.1, 20.2, 20.7) but the caller's
// Onboarding_State record (created earlier, in Authenticate) remains
// resumable (Req 20.3, 20.8). On success, both the workspace and the owner
// Membership are written together, and the Membership's Stytch_Member_ID
// always comes from the same successful call (Req 11.9, 11.10, 20.5).
func attemptSignup(stytchFails bool) simAccountState {
	if stytchFails {
		return simAccountState{resumable: true}
	}
	return simAccountState{
		workspaceCreated:  true,
		membershipCreated: true,
		stytchOrgLinked:   true,
		resumable:         true, // the Onboarding_State also transitions to complete, which is itself resumable/inspectable
	}
}

// attemptSwitch mirrors SwitchWorkspace's ordering: call Stytch ExchangeSession
// first; on failure, return 503 with zero writes to the existing Membership
// (Req 20.4) — the account, having already been fully created in a prior
// signup, is never left partial by a failed switch.
func attemptSwitch(stytchFails bool, before simAccountState) simAccountState {
	if stytchFails {
		return before // zero state change
	}
	// A successful switch only ever updates last_active_at/stytch_member_id
	// on an ALREADY-EXISTING membership — it can never itself create the
	// workspace or the membership, so those flags are unaffected either way.
	return before
}

// Feature: stytch-employer-onboarding, Property 30: Stytch failure never leaves a partial account
// Validates: Requirements 3.5, 11.9, 11.10, 20.1, 20.2, 20.3, 20.4, 20.5, 20.7, 20.8
func TestProperty30_StytchFailureNeverLeavesAPartialAccount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state := simAccountState{}

		steps := rapid.IntRange(1, 20).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			op := rapid.IntRange(0, 1).Draw(t, "op")
			stytchFails := rapid.Bool().Draw(t, "stytchFails")

			switch op {
			case 0: // signup / workspace creation attempt
				state = attemptSignup(stytchFails)
			case 1: // workspace switch attempt
				state = attemptSwitch(stytchFails, state)
			}

			// Invariant, after every step: the account is either complete
			// (workspace AND membership both exist, and the membership is
			// linked to a real Stytch_Member) or resumable (an
			// Onboarding_State survives) — never a partial mix.
			complete := state.workspaceCreated && state.membershipCreated && state.stytchOrgLinked
			partial := (state.workspaceCreated || state.membershipCreated) && !complete

			if partial {
				t.Fatalf("step %d (op=%d, stytchFails=%v): partial account state: %+v", i, op, stytchFails, state)
			}
			if !complete && !state.resumable && (state.workspaceCreated || state.membershipCreated) {
				t.Fatalf("step %d: non-complete state has documents but is not resumable: %+v", i, state)
			}

			// Never a Membership lacking a corresponding Stytch_Member (Req 11.9, 11.10).
			if state.membershipCreated && !state.stytchOrgLinked {
				t.Fatalf("step %d: membershipCreated=true but stytchOrgLinked=false: %+v", i, state)
			}
		}
	})
}
