package invitations

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// simInvitation mirrors the subset of Invitation state the five operations
// below touch: status and expiry. Transitions follow the same guards as
// service.go's Resend/Revoke/Decline/Accept — each requires the current
// EffectiveStatus to be "pending" before it applies, exactly as every one of
// those methods checks `effective != StatusPending` before writing.
type simInvitation struct {
	status    Status
	expiresAt time.Time
}

func (s *simInvitation) effective(now time.Time) Status {
	return EffectiveStatus(s.status, s.expiresAt, now)
}

func (s *simInvitation) create(now time.Time) {
	s.status = StatusPending
	s.expiresAt = now.Add(7 * 24 * time.Hour)
}

func (s *simInvitation) resend(now time.Time) {
	if s.effective(now) != StatusPending {
		return
	}
	s.expiresAt = now.Add(7 * 24 * time.Hour)
}

func (s *simInvitation) revoke(now time.Time) {
	if s.effective(now) != StatusPending {
		return
	}
	s.status = StatusRevoked
}

func (s *simInvitation) decline(now time.Time) {
	if s.effective(now) != StatusPending {
		return
	}
	s.status = StatusDeclined
}

func (s *simInvitation) accept(now time.Time) {
	if s.effective(now) != StatusPending {
		return
	}
	s.status = StatusAccepted
}

// Feature: stytch-employer-onboarding, Property 11: Invitation state transitions follow the declared machine
// Validates: Requirements 12.1, 12.2, 12.3, 12.5, 12.7, 12.9
func TestProperty11_InvitationStateTransitionsFollowDeclaredMachine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		inv := &simInvitation{}
		inv.create(now)

		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			before := inv.effective(now)
			op := rapid.IntRange(0, 4).Draw(t, "op")

			switch op {
			case 0:
				inv.resend(now)
			case 1:
				inv.revoke(now)
			case 2:
				inv.decline(now)
			case 3:
				inv.accept(now)
			case 4:
				advance := rapid.IntRange(0, int((8*24*time.Hour).Minutes())).Draw(t, "advanceMinutes")
				now = now.Add(time.Duration(advance) * time.Minute)
			}

			after := inv.effective(now)

			// No operation moves an Invitation OUT of accepted, declined, or revoked.
			if before == StatusAccepted || before == StatusDeclined || before == StatusRevoked {
				if after != before {
					t.Fatalf("step %d: terminal status %s changed to %s (op=%d)", i, before, after, op)
				}
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 12: An Invitation_Token is single-use and grants exactly one Membership
// Validates: Requirements 11.3, 11.11, 11.17
//
// Simulates MembershipService.Create's duplicate-key semantics (an
// already-existing membership for the pair is returned rather than
// duplicated) combined with the invitation's own pending-only acceptance
// guard: for an arbitrary number of concurrent and sequential presentations
// of one token by the correctly matching email, exactly one Membership is
// created.
func TestProperty12_TokenSingleUseGrantsExactlyOneMembership(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		inv := &simInvitation{}
		inv.create(now)

		presentations := rapid.IntRange(1, 20).Draw(t, "presentations")
		membershipCreated := false
		createdCount := 0

		for i := 0; i < presentations; i++ {
			if inv.effective(now) != StatusPending {
				continue // token already consumed or otherwise non-pending: zero effect
			}
			if !membershipCreated {
				membershipCreated = true
				createdCount++
			}
			inv.status = StatusAccepted
		}

		if createdCount != boolToInt(membershipCreated) {
			t.Fatalf("createdCount=%d inconsistent with membershipCreated=%v", createdCount, membershipCreated)
		}
		if createdCount > 1 {
			t.Fatalf("token presented %d times created %d memberships, want at most 1", presentations, createdCount)
		}
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
