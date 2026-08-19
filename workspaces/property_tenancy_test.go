package workspaces

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"go.mongodb.org/mongo-driver/bson"
)

// tenancyDoc mirrors the fields EmployerReadFilter/EmployerWriteFilter act on.
type tenancyDoc struct {
	ID          string
	OwnerID     string
	WorkspaceID string // "" means the field is absent, matching $exists:false
}

// matchesFilter evaluates a bson.M built by EmployerReadFilter/
// EmployerWriteFilter against a tenancyDoc, mirroring MongoDB's matching
// semantics for the small filter shapes these two functions produce:
// equality on workspace_id/owner_id/_id, and $exists:false on workspace_id.
func matchesFilter(filter bson.M, d tenancyDoc) bool {
	for key, val := range filter {
		switch key {
		case "_id":
			if d.ID != val {
				return false
			}
		case "owner_id":
			if d.OwnerID != val {
				return false
			}
		case "workspace_id":
			switch v := val.(type) {
			case string:
				if d.WorkspaceID != v {
					return false
				}
			case bson.M:
				if exists, ok := v["$exists"].(bool); ok {
					hasField := d.WorkspaceID != ""
					if exists != hasField {
						return false
					}
				}
			}
		}
	}
	return true
}

// Feature: stytch-employer-onboarding, Property 16: Tenancy isolation holds for every generated access pattern
// Validates: Requirements 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 9.9, 23.5, 24.8
func TestProperty16_TenancyIsolationHoldsForEveryGeneratedAccessPattern(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		wsA := "ws-a"
		wsB := "ws-b"
		ownerA := "owner-a"
		ownerB := "owner-b"

		docCount := rapid.IntRange(0, 20).Draw(t, "docCount")
		var docs []tenancyDoc
		for i := 0; i < docCount; i++ {
			kind := rapid.SampledFrom([]string{"scoped_a", "scoped_b", "unscoped_owned_by_a", "unscoped_owned_by_b"}).Draw(t, "docKind")
			id := "doc-" + strconv.Itoa(i)
			switch kind {
			case "scoped_a":
				docs = append(docs, tenancyDoc{ID: id, OwnerID: ownerA, WorkspaceID: wsA})
			case "scoped_b":
				docs = append(docs, tenancyDoc{ID: id, OwnerID: ownerB, WorkspaceID: wsB})
			case "unscoped_owned_by_a":
				docs = append(docs, tenancyDoc{ID: id, OwnerID: ownerA, WorkspaceID: ""})
			case "unscoped_owned_by_b":
				docs = append(docs, tenancyDoc{ID: id, OwnerID: ownerB, WorkspaceID: ""})
			}
		}

		// Reading under Workspace A's context returns zero documents belonging to Workspace B.
		readFilterA := EmployerReadFilter(wsA, ownerA)
		for _, d := range docs {
			if matchesFilter(readFilterA, d) && d.WorkspaceID == wsB {
				t.Fatalf("read under workspace %q matched a document scoped to %q: %+v", wsA, wsB, d)
			}
		}
		readFilterB := EmployerReadFilter(wsB, ownerB)
		for _, d := range docs {
			if matchesFilter(readFilterB, d) && d.WorkspaceID == wsA {
				t.Fatalf("read under workspace %q matched a document scoped to %q: %+v", wsB, wsA, d)
			}
		}

		// A write targeting a document in a different workspace matches zero documents (404).
		for _, d := range docs {
			if d.WorkspaceID == wsB {
				writeFilterFromA := EmployerWriteFilter(wsA, ownerA, d.ID)
				if matchesFilter(writeFilterFromA, d) {
					t.Fatalf("write under workspace %q matched a document scoped to %q: %+v", wsA, wsB, d)
				}
			}
		}

		// A document holding no workspace_id is visible only to the users
		// document named by its owner_id — i.e. only the legacy (workspaceID="")
		// filter for that exact owner matches it, and no workspace-scoped
		// filter (for A or B) ever matches an unscoped document.
		for _, d := range docs {
			if d.WorkspaceID != "" {
				continue
			}
			if matchesFilter(readFilterA, d) {
				t.Fatalf("workspace-scoped read for %q matched unscoped doc %+v", wsA, d)
			}
			if matchesFilter(readFilterB, d) {
				t.Fatalf("workspace-scoped read for %q matched unscoped doc %+v", wsB, d)
			}
			legacyFilterOwner := EmployerReadFilter("", d.OwnerID)
			if !matchesFilter(legacyFilterOwner, d) {
				t.Fatalf("legacy read filter for the doc's own owner %q did not match unscoped doc %+v", d.OwnerID, d)
			}
			otherOwner := ownerA
			if d.OwnerID == ownerA {
				otherOwner = ownerB
			}
			legacyFilterOther := EmployerReadFilter("", otherOwner)
			if matchesFilter(legacyFilterOther, d) {
				t.Fatalf("legacy read filter for a different owner %q matched unscoped doc %+v", otherOwner, d)
			}
		}
	})
}
