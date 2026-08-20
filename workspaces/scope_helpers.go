package workspaces

import "go.mongodb.org/mongo-driver/bson"

// EmployerReadFilter builds a MongoDB filter for workspace-scoped employer reads.
// If a workspace_id is provided (from the session context), it filters by workspace_id.
// If workspace_id is empty (legacy path), it filters by owner_id only.
// A document holding no workspace_id remains visible only to the owner (Req 9.7).
//
// Requirements: 9.2, 9.5, 9.6, 9.7
func EmployerReadFilter(workspaceID, ownerID string) bson.M {
	if workspaceID != "" {
		return bson.M{"workspace_id": workspaceID}
	}
	// Legacy path: unscoped documents visible only to their owner.
	return bson.M{
		"owner_id":     ownerID,
		"workspace_id": bson.M{"$exists": false},
	}
}

// EmployerWriteFilter builds a MongoDB filter for workspace-scoped employer writes.
// Ensures a write targeting a document in a different workspace gets zero results (404).
// Requirements: 9.3, 9.4
func EmployerWriteFilter(workspaceID, ownerID, documentID string) bson.M {
	if workspaceID != "" {
		return bson.M{
			"_id":          documentID,
			"workspace_id": workspaceID,
		}
	}
	return bson.M{
		"_id":      documentID,
		"owner_id": ownerID,
	}
}

// EmployerCreateDoc returns the bson.M fields that should be set on a new
// workspace-scoped document at creation time. If workspaceID is empty (legacy),
// returns nil (no workspace_id set, which is the omitempty behavior).
//
// DEFERRED SCAFFOLDING — no caller yet; do not delete as dead code. This is the
// write-side counterpart to EmployerReadFilter/EmployerWriteFilter above, and it
// gets wired by spec tasks 13.2 and 13.4 (the `jobrequests` company/vacancy
// write handlers and `quotation_attachments` creation) so every document created
// after this feature ships carries a workspace_id (Req 9.1). Task 13 is
// deliberately deferred because it rewrites live production endpoints.
//
// Requirements: 9.1
func EmployerCreateDoc(workspaceID string) bson.M {
	if workspaceID == "" {
		return nil
	}
	return bson.M{"workspace_id": workspaceID}
}
