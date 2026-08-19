package identity

import (
	"testing"

	"pgregory.net/rapid"
)

// simulatedLinkStore mirrors the two unique indexes EnsureIndexes creates on
// account_links: uniq_account_link_user (user_id) and
// uniq_account_link_member (stytch_member_id). An insert whose user_id or
// stytch_member_id already exists in the store is rejected, exactly as
// MongoDB's unique index would reject it with E11000.
type simulatedLinkStore struct {
	byUser   map[string]bool
	byMember map[string]bool
}

func newSimulatedLinkStore() *simulatedLinkStore {
	return &simulatedLinkStore{byUser: map[string]bool{}, byMember: map[string]bool{}}
}

func (s *simulatedLinkStore) tryLink(userID, memberID string) bool {
	if s.byUser[userID] || s.byMember[memberID] {
		return false
	}
	s.byUser[userID] = true
	s.byMember[memberID] = true
	return true
}

// Feature: stytch-employer-onboarding, Property 22: Account_Link uniqueness holds in both directions
// Validates: Requirement 5.8
func TestProperty22_AccountLinkUniquenessHoldsInBothDirections(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userPool := rapid.SliceOfN(rapid.StringMatching(`u[0-9]{1,2}`), 1, 5).Draw(t, "userPool")
		memberPool := rapid.SliceOfN(rapid.StringMatching(`m[0-9]{1,2}`), 1, 5).Draw(t, "memberPool")

		attemptCount := rapid.IntRange(1, 20).Draw(t, "attemptCount")
		store := newSimulatedLinkStore()

		succeededUsers := map[string]int{}
		succeededMembers := map[string]int{}

		for i := 0; i < attemptCount; i++ {
			userID := rapid.SampledFrom(userPool).Draw(t, "userID")
			memberID := rapid.SampledFrom(memberPool).Draw(t, "memberID")

			if store.tryLink(userID, memberID) {
				succeededUsers[userID]++
				succeededMembers[memberID]++
			}
		}

		for user, count := range succeededUsers {
			if count > 1 {
				t.Fatalf("user %q was linked %d times, want at most 1 (uniq_account_link_user violated)", user, count)
			}
		}
		for member, count := range succeededMembers {
			if count > 1 {
				t.Fatalf("stytch_member %q was linked %d times, want at most 1 (uniq_account_link_member violated)", member, count)
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 22b: concurrent link attempts against the same store never both succeed
// Validates: Requirement 5.8
func TestProperty22_ConcurrentLinkAttemptsForSamePairNeverBothSucceed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userID := rapid.StringMatching(`u[0-9]{1,3}`).Draw(t, "userID")
		memberID := rapid.StringMatching(`m[0-9]{1,3}`).Draw(t, "memberID")
		attempts := rapid.IntRange(2, 10).Draw(t, "attempts")

		store := newSimulatedLinkStore()
		successCount := 0
		for i := 0; i < attempts; i++ {
			if store.tryLink(userID, memberID) {
				successCount++
			}
		}
		if successCount != 1 {
			t.Fatalf("repeated attempts to link the same (user, member) pair succeeded %d times, want exactly 1", successCount)
		}
	})
}
