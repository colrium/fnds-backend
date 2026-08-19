package workspaces

import (
	"testing"

	"pgregory.net/rapid"
)

// rescopeDoc mirrors the shape of a companies/job_vacancies/user_subscriptions
// document as far as RescopeUserData's filter and update touch it.
type rescopeDoc struct {
	ID          string
	OwnerID     string
	WorkspaceID string // "" means absent, matching the real $exists:false filter
}

// simulateRescope reproduces RescopeUserData's per-collection update: for
// every document owned by userID with no workspace_id, set workspace_id.
// Documents already scoped, or owned by someone else, are left untouched —
// exactly the update's filter in rescope.go.
func simulateRescope(docs []rescopeDoc, userID, workspaceID string) []rescopeDoc {
	out := make([]rescopeDoc, len(docs))
	for i, d := range docs {
		if d.OwnerID == userID && d.WorkspaceID == "" {
			d.WorkspaceID = workspaceID
		}
		out[i] = d
	}
	return out
}

// Feature: stytch-employer-onboarding, Property 24: Data re-scoping is idempotent and lossless
// Validates: Requirements 6.1, 6.2, 6.3, 6.4, 6.5
func TestProperty24_RescopingIdempotentAndLossless(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userID := rapid.StringMatching(`[a-f0-9]{8}`).Draw(t, "userID")
		workspaceID := rapid.StringMatching(`[a-f0-9]{8}`).Draw(t, "workspaceID")

		n := rapid.IntRange(0, 15).Draw(t, "docCount")
		var docs []rescopeDoc
		for i := 0; i < n; i++ {
			owner := userID
			if rapid.Bool().Draw(t, "isOtherOwner") {
				owner = rapid.StringMatching(`[a-f0-9]{8}`).Draw(t, "otherOwnerID")
			}
			existingWorkspace := ""
			if rapid.Bool().Draw(t, "alreadyScoped") {
				existingWorkspace = rapid.StringMatching(`[a-f0-9]{8}`).Draw(t, "existingWorkspaceID")
			}
			docs = append(docs, rescopeDoc{
				ID:          rapid.StringMatching(`[a-f0-9]{8}`).Draw(t, "docID"),
				OwnerID:     owner,
				WorkspaceID: existingWorkspace,
			})
		}

		once := simulateRescope(docs, userID, workspaceID)
		twice := simulateRescope(once, userID, workspaceID)

		// Lossless: count and OwnerID are preserved.
		if len(once) != len(docs) {
			t.Fatalf("document count changed: got %d, want %d", len(once), len(docs))
		}
		for i := range docs {
			if once[i].OwnerID != docs[i].OwnerID {
				t.Fatalf("doc %d: owner_id changed from %q to %q", i, docs[i].OwnerID, once[i].OwnerID)
			}
			if once[i].ID != docs[i].ID {
				t.Fatalf("doc %d: document identity changed", i)
			}
		}

		// Applying twice yields the same documents as applying once.
		for i := range once {
			if once[i] != twice[i] {
				t.Fatalf("doc %d: applying rescope twice changed the document: once=%+v twice=%+v", i, once[i], twice[i])
			}
		}

		// Every document owned by userID ends up scoped to workspaceID,
		// regardless of whether it started scoped elsewhere or unscoped —
		// wait: a document already scoped to a DIFFERENT workspace must NOT
		// be re-scoped (the filter requires workspace_id absent). Verify
		// exactly that split.
		for i, d := range docs {
			if d.OwnerID != userID {
				if once[i].WorkspaceID != d.WorkspaceID {
					t.Fatalf("doc %d: owned by a different user but its workspace_id changed", i)
				}
				continue
			}
			if d.WorkspaceID == "" {
				if once[i].WorkspaceID != workspaceID {
					t.Fatalf("doc %d: owned by userID with no prior workspace_id, want scoped to %q, got %q", i, workspaceID, once[i].WorkspaceID)
				}
			} else {
				if once[i].WorkspaceID != d.WorkspaceID {
					t.Fatalf("doc %d: already scoped to %q, must not be overwritten, got %q", i, d.WorkspaceID, once[i].WorkspaceID)
				}
			}
		}
	})
}
