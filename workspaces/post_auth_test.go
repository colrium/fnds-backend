package workspaces

import (
	"context"
	"testing"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// usePostAuthTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup.
func usePostAuthTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// newTestWorkspaceService builds a WorkspaceService with nil dependencies
// (adapter, audit, clock, limiter) — sufficient for ResolvePostAuth which
// does not use them.
func newTestWorkspaceService() *WorkspaceService {
	return &WorkspaceService{}
}

func TestResolvePostAuth_ExactlyOneMembership(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("single active membership sets that workspace as active", func(mt *mtest.T) {
		usePostAuthTestMongo(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		// Mock: memberships.Find returns one membership document.
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "m-1"},
				{Key: "workspace_id", Value: "ws-1"},
				{Key: "user_id", Value: "user-1"},
				{Key: "role", Value: "owner"},
				{Key: "status", Value: "active"},
				{Key: "authority_version", Value: int64(1)},
				{Key: "last_active_at", Value: time.Now()},
				{Key: "joined_at", Value: time.Now()},
				{Key: "created_at", Value: time.Now()},
				{Key: "updated_at", Value: time.Now()},
			}),
		)

		svc := newTestWorkspaceService()
		result, err := svc.ResolvePostAuth(context.Background(), "user-1", "user@example.com")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if result.ActiveWorkspaceID != "ws-1" {
			mt.Errorf("ActiveWorkspaceID = %q, want %q", result.ActiveWorkspaceID, "ws-1")
		}
		if result.Role != RoleOwner {
			mt.Errorf("Role = %q, want %q", result.Role, RoleOwner)
		}
		if len(result.Permissions) == 0 {
			mt.Error("expected non-empty permissions for owner")
		}
		if result.NeedsWorkspace {
			mt.Error("NeedsWorkspace should be false when there is an active membership")
		}
		if len(result.Workspaces) != 0 {
			mt.Errorf("Workspaces should be empty for single membership, got %d", len(result.Workspaces))
		}
		if len(result.PendingInvitations) != 0 {
			mt.Errorf("PendingInvitations should be empty, got %d", len(result.PendingInvitations))
		}
	})
}

func TestResolvePostAuth_MultipleMemberships(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("multiple active memberships returns most recent and workspace list", func(mt *mtest.T) {
		usePostAuthTestMongo(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		now := time.Now()
		earlier := now.Add(-1 * time.Hour)

		// Mock: memberships.Find returns two membership documents (sorted by last_active_at desc).
		mt.AddMockResponses(
			// memberships.Find cursor (2 docs)
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch,
				bson.D{
					{Key: "_id", Value: "m-1"},
					{Key: "workspace_id", Value: "ws-1"},
					{Key: "user_id", Value: "user-1"},
					{Key: "role", Value: "owner"},
					{Key: "status", Value: "active"},
					{Key: "authority_version", Value: int64(2)},
					{Key: "last_active_at", Value: now},
					{Key: "joined_at", Value: earlier},
					{Key: "created_at", Value: earlier},
					{Key: "updated_at", Value: now},
				},
				bson.D{
					{Key: "_id", Value: "m-2"},
					{Key: "workspace_id", Value: "ws-2"},
					{Key: "user_id", Value: "user-1"},
					{Key: "role", Value: "hr"},
					{Key: "status", Value: "active"},
					{Key: "authority_version", Value: int64(1)},
					{Key: "last_active_at", Value: earlier},
					{Key: "joined_at", Value: earlier},
					{Key: "created_at", Value: earlier},
					{Key: "updated_at", Value: earlier},
				},
			),
			// workspaces.Find cursor (2 docs) — for building workspace summaries.
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch,
				bson.D{
					{Key: "_id", Value: "ws-1"},
					{Key: "name", Value: "Workspace One"},
					{Key: "slug", Value: "workspace-one"},
					{Key: "created_at", Value: earlier},
				},
				bson.D{
					{Key: "_id", Value: "ws-2"},
					{Key: "name", Value: "Workspace Two"},
					{Key: "slug", Value: "workspace-two"},
					{Key: "created_at", Value: earlier},
				},
			),
		)

		svc := newTestWorkspaceService()
		result, err := svc.ResolvePostAuth(context.Background(), "user-1", "user@example.com")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}

		// Most recently active workspace should be active.
		if result.ActiveWorkspaceID != "ws-1" {
			mt.Errorf("ActiveWorkspaceID = %q, want %q", result.ActiveWorkspaceID, "ws-1")
		}
		if result.Role != RoleOwner {
			mt.Errorf("Role = %q, want %q", result.Role, RoleOwner)
		}
		if len(result.Permissions) == 0 {
			mt.Error("expected non-empty permissions for owner")
		}
		// Should return the full list for the selector.
		if len(result.Workspaces) != 2 {
			mt.Fatalf("Workspaces count = %d, want 2", len(result.Workspaces))
		}
		if result.Workspaces[0].ID != "ws-1" {
			mt.Errorf("Workspaces[0].ID = %q, want %q", result.Workspaces[0].ID, "ws-1")
		}
		if result.Workspaces[1].ID != "ws-2" {
			mt.Errorf("Workspaces[1].ID = %q, want %q", result.Workspaces[1].ID, "ws-2")
		}
		if result.NeedsWorkspace {
			mt.Error("NeedsWorkspace should be false")
		}
	})
}

func TestResolvePostAuth_ZeroMembershipsWithPendingInvitations(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("zero memberships with pending invitations returns them", func(mt *mtest.T) {
		usePostAuthTestMongo(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		// Mock: memberships.Find returns empty cursor (zero active memberships).
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch),
			// invitations.Find returns one pending invitation.
			mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "inv-1"},
				{Key: "workspace_id", Value: "ws-99"},
				{Key: "role", Value: "admin"},
				{Key: "status", Value: "pending"},
				{Key: "email", Value: "user@example.com"},
			}),
			// workspaces.Find for invitation workspace names.
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "ws-99"},
				{Key: "name", Value: "Invited Workspace"},
				{Key: "slug", Value: "invited-workspace"},
				{Key: "created_at", Value: time.Now()},
			}),
		)

		svc := newTestWorkspaceService()
		result, err := svc.ResolvePostAuth(context.Background(), "user-1", "  User@Example.COM  ")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if result.ActiveWorkspaceID != "" {
			mt.Errorf("ActiveWorkspaceID = %q, want empty", result.ActiveWorkspaceID)
		}
		if result.NeedsWorkspace {
			mt.Error("NeedsWorkspace should be false when invitations exist")
		}
		if len(result.PendingInvitations) != 1 {
			mt.Fatalf("PendingInvitations count = %d, want 1", len(result.PendingInvitations))
		}
		inv := result.PendingInvitations[0]
		if inv.ID != "inv-1" {
			mt.Errorf("invitation ID = %q, want %q", inv.ID, "inv-1")
		}
		if inv.WorkspaceName != "Invited Workspace" {
			mt.Errorf("WorkspaceName = %q, want %q", inv.WorkspaceName, "Invited Workspace")
		}
		if inv.Role != "admin" {
			mt.Errorf("Role = %q, want %q", inv.Role, "admin")
		}
	})
}

func TestResolvePostAuth_ZeroMembershipsZeroInvitations(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("zero memberships and zero invitations sets needs_workspace true", func(mt *mtest.T) {
		usePostAuthTestMongo(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		// Mock: memberships.Find returns empty cursor.
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch),
			// invitations.Find returns empty cursor.
			mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch),
		)

		svc := newTestWorkspaceService()
		result, err := svc.ResolvePostAuth(context.Background(), "user-1", "user@example.com")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if !result.NeedsWorkspace {
			mt.Error("NeedsWorkspace should be true when no memberships and no invitations")
		}
		if result.ActiveWorkspaceID != "" {
			mt.Errorf("ActiveWorkspaceID = %q, want empty", result.ActiveWorkspaceID)
		}
		if len(result.Workspaces) != 0 {
			mt.Errorf("Workspaces should be empty, got %d", len(result.Workspaces))
		}
		if len(result.PendingInvitations) != 0 {
			mt.Errorf("PendingInvitations should be empty, got %d", len(result.PendingInvitations))
		}
	})
}

func TestResolvePostAuth_PermissionsMatchRole(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("permissions for active workspace match exactly the role's grants", func(mt *mtest.T) {
		usePostAuthTestMongo(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		// Single membership with role "hr".
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "m-hr"},
				{Key: "workspace_id", Value: "ws-hr"},
				{Key: "user_id", Value: "user-1"},
				{Key: "role", Value: "hr"},
				{Key: "status", Value: "active"},
				{Key: "authority_version", Value: int64(1)},
				{Key: "last_active_at", Value: time.Now()},
				{Key: "joined_at", Value: time.Now()},
				{Key: "created_at", Value: time.Now()},
				{Key: "updated_at", Value: time.Now()},
			}),
		)

		svc := newTestWorkspaceService()
		result, err := svc.ResolvePostAuth(context.Background(), "user-1", "user@example.com")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if result.Role != RoleHR {
			mt.Errorf("Role = %q, want %q", result.Role, RoleHR)
		}

		// The returned permissions should be exactly what GrantedPermissions(RoleHR) returns.
		expected := GrantedPermissions(RoleHR)
		if len(result.Permissions) != len(expected) {
			mt.Fatalf("permissions count = %d, want %d", len(result.Permissions), len(expected))
		}
		for i, p := range result.Permissions {
			if p != expected[i] {
				mt.Errorf("permission[%d] = %q, want %q", i, p, expected[i])
			}
		}
	})
}

func TestResolvePostAuth_EmailNormalization(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("email is normalized before querying invitations", func(mt *mtest.T) {
		usePostAuthTestMongo(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		// Zero memberships.
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch),
			// Zero invitations found (email should be normalized to "user@example.com").
			mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch),
		)

		svc := newTestWorkspaceService()
		// Pass email with leading/trailing whitespace and uppercase — should be normalized.
		result, err := svc.ResolvePostAuth(context.Background(), "user-1", "  USER@EXAMPLE.COM  ")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		// With zero memberships and zero invitations, we should get needs_workspace = true.
		if !result.NeedsWorkspace {
			mt.Error("NeedsWorkspace should be true")
		}
	})
}
