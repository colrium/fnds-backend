package workspaces

import (
	"sort"
	"strings"

	"backend/identity"
	"backend/users"
)

// MigrationCandidate is a user eligible for migration plus metadata.
type MigrationCandidate struct {
	User          users.User
	WorkspaceName string
}

// MigrationCollision records two or more users sharing one normalized email.
// Req 21.7 forbids picking a winner automatically, so every colliding user ID is
// listed and none of them are migrated until an administrator resolves it.
type MigrationCollision struct {
	NormalizedEmail string
	UserIDs         []string
}

// CandidatesResult holds the output of the Candidates function.
type CandidatesResult struct {
	Eligible   []MigrationCandidate
	Collisions []MigrationCollision
	NoEmail    []string // user IDs with empty email

	// Truncated reports that the customer-user scan hit its page cap, so users
	// beyond it were never evaluated. Callers must surface this: a report that
	// omits it reads as though every user was considered.
	Truncated bool
}

// OwnershipInfo provides company/vacancy ownership counts for a user.
type OwnershipInfo struct {
	Companies         int
	Vacancies         int
	NewestCompanyName string
}

// Candidates applies the migration eligibility rule once, shared by the
// implementation and the property test:
//   - user_type == "customer" (Req 21.14)
//   - non-empty email after normalization (Req 21.6)
//   - at least one companies or job_vacancies document owned (Req 21.15)
//   - no existing Account_Link (Req 21.5)
//
// Users sharing one normalized email after Email_Normalization are a
// collision: none of them are migrated automatically. Req 21.7 forbids
// picking a "most recent" winner, because an automatic choice can silently
// orphan the data owned by the other colliding accounts. Every colliding user
// ID is reported so an administrator can resolve the collision manually.
//
// The ownershipFn parameter allows the caller to provide ownership info per user
// (how many companies/vacancies they own and the newest company name).
// The linkedFn parameter reports whether a user already has an Account_Link.
func Candidates(
	allUsers []users.User,
	ownershipFn func(userID string) OwnershipInfo,
	linkedFn func(userID string) bool,
) CandidatesResult {
	var result CandidatesResult

	// Group eligible users by normalized email.
	type eligible struct {
		user      users.User
		ownership OwnershipInfo
	}
	byEmail := make(map[string][]eligible)

	for _, u := range allUsers {
		// Req 21.14: user_type must be "customer"
		if u.UserType != "customer" {
			continue
		}

		// Req 21.6: non-empty email after normalization
		normalized := identity.NormalizeEmail(u.Email)
		if normalized == "" {
			result.NoEmail = append(result.NoEmail, u.ID)
			continue
		}

		// Req 21.5: no existing Account_Link
		if linkedFn(u.ID) {
			continue
		}

		// Req 21.15: at least one company or vacancy
		ownership := ownershipFn(u.ID)
		if ownership.Companies == 0 && ownership.Vacancies == 0 {
			continue
		}

		byEmail[normalized] = append(byEmail[normalized], eligible{user: u, ownership: ownership})
	}

	// Report collisions without picking a winner (Req 21.7); a single
	// non-colliding candidate is eligible immediately.
	for email, candidates := range byEmail {
		if len(candidates) == 1 {
			c := candidates[0]
			result.Eligible = append(result.Eligible, MigrationCandidate{
				User:          c.user,
				WorkspaceName: DeriveWorkspaceName(c.user, c.ownership.NewestCompanyName),
			})
			continue
		}

		// Sort by created_at descending so the report lists collisions in a
		// stable, predictable order for the administrator resolving them.
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].user.CreatedAt.After(candidates[j].user.CreatedAt)
		})

		userIDs := make([]string, 0, len(candidates))
		for _, c := range candidates {
			userIDs = append(userIDs, c.user.ID)
		}
		result.Collisions = append(result.Collisions, MigrationCollision{
			NormalizedEmail: email,
			UserIDs:         userIDs,
		})
	}

	return result
}

// DeriveWorkspaceName implements Req 21.3: uses the most recently created
// company's name, falling back to first+last name when no companies exist.
func DeriveWorkspaceName(u users.User, newestCompanyName string) string {
	if newestCompanyName != "" {
		return newestCompanyName
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		return "Workspace"
	}
	return name
}
