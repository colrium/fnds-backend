package workspaces

import (
	"testing"
	"time"

	"backend/identity"
	"backend/users"

	"pgregory.net/rapid"
)

// genEmailVariant draws a base local part and domain, then applies a
// transformation drawn from {none, case-flip, pad-whitespace} — the
// variants that Email_Normalization DOES collapse to equal values (Req 4.1).
// Plus-suffix and dot-placement are deliberately excluded here since P3
// already covers that they must NOT collapse.
func genEmailVariant(t *rapid.T, base string) string {
	kind := rapid.IntRange(0, 2).Draw(t, "variantKind")
	switch kind {
	case 0:
		return base
	case 1:
		return " " + base + "  "
	default:
		out := make([]rune, 0, len(base))
		for _, r := range base {
			if r >= 'a' && r <= 'z' && rapid.Bool().Draw(t, "flip") {
				out = append(out, r-32)
			} else {
				out = append(out, r)
			}
		}
		return string(out)
	}
}

// Feature: stytch-employer-onboarding, Property 23: Linking never creates a duplicate user
// Validates: Requirements 4.4, 4.6, 5.12
//
// Candidates groups users by normalized email and, for the purposes of the
// migration path (Req 21.7), never merges more than one pre-existing users
// document into a single migrated record — every colliding document is
// reported, none are silently combined. This property checks the general
// form of that guarantee: for an arbitrary population of users whose emails
// vary only by case and surrounding whitespace (i.e. they normalize to the
// same value), Candidates produces at most one eligible MigrationCandidate
// per normalized email — collisions are reported, never merged into more
// than one eligible record — so the count of users documents a caller might
// go on to create (one per eligible candidate) never exceeds the number of
// distinct normalized-email groups.
func TestProperty23_LinkingNeverCreatesDuplicateUser(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		distinctEmails := rapid.IntRange(1, 6).Draw(t, "distinctEmailCount")
		bases := make([]string, distinctEmails)
		for i := range bases {
			bases[i] = rapid.StringMatching(`[a-z]{3,10}@[a-z]{3,10}\.[a-z]{2,4}`).Draw(t, "baseEmail")
		}

		userCount := rapid.IntRange(0, 15).Draw(t, "userCount")
		var allUsers []users.User
		now := time.Now()
		for i := 0; i < userCount; i++ {
			baseIdx := rapid.IntRange(0, distinctEmails-1).Draw(t, "baseIdx")
			email := genEmailVariant(t, bases[baseIdx])
			allUsers = append(allUsers, users.User{
				ID:        rapid.StringMatching(`[a-f0-9]{8}`).Draw(t, "userID"),
				Email:     email,
				UserType:  "customer",
				CreatedAt: now.Add(time.Duration(i) * time.Second),
			})
		}

		result := Candidates(allUsers,
			func(userID string) OwnershipInfo { return OwnershipInfo{Companies: 1} }, // every user "owns" something, so ownership never excludes them
			func(userID string) bool { return false },                                // nobody is already linked
		)

		normalizedGroups := make(map[string]int)
		for _, u := range allUsers {
			if u.UserType != "customer" {
				continue
			}
			n := identity.NormalizeEmail(u.Email)
			if n == "" {
				continue
			}
			normalizedGroups[n]++
		}

		eligibleByEmail := make(map[string]int)
		for _, c := range result.Eligible {
			n := identity.NormalizeEmail(c.User.Email)
			eligibleByEmail[n]++
		}
		for email, count := range eligibleByEmail {
			if count > 1 {
				t.Fatalf("normalized email %q produced %d eligible candidates, want at most 1 (duplicate user risk)", email, count)
			}
		}

		// Every normalized-email group with more than one member must be
		// reported as a collision, not silently resolved into one eligible
		// candidate that hides the others' existence.
		collisionEmails := make(map[string]bool)
		for _, c := range result.Collisions {
			collisionEmails[c.NormalizedEmail] = true
			if len(c.UserIDs) < 2 {
				t.Fatalf("collision for %q reports only %d user IDs, want >= 2", c.NormalizedEmail, len(c.UserIDs))
			}
		}
		for email, count := range normalizedGroups {
			if count > 1 && !collisionEmails[email] && eligibleByEmail[email] > 0 {
				t.Fatalf("normalized email %q has %d users but was neither reported as a collision nor excluded", email, count)
			}
		}
	})
}
