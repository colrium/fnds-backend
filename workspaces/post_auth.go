package workspaces

import (
	"context"
	"fmt"

	"backend/configs"
	"backend/identity"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PostAuthResult describes the workspace state after authentication.
type PostAuthResult struct {
	// ActiveWorkspaceID is the workspace to activate (empty if none).
	ActiveWorkspaceID string `json:"active_workspace_id,omitempty"`
	// Role is the user's role in the active workspace.
	Role Role `json:"role,omitempty"`
	// Permissions is the granted permission set for the active workspace.
	Permissions []Permission `json:"permissions,omitempty"`
	// Workspaces lists all active workspaces (for the selector).
	Workspaces []WorkspaceSummary `json:"workspaces,omitempty"`
	// NeedsWorkspace is true when the user should create a workspace.
	NeedsWorkspace bool `json:"needs_workspace,omitempty"`
	// PendingInvitations lists pending invitations for the user's email.
	PendingInvitations []PendingInvitationView `json:"pending_invitations,omitempty"`
}

// PendingInvitationView is a minimal view of a pending invitation for post-auth display.
type PendingInvitationView struct {
	ID            string `json:"id"`
	WorkspaceName string `json:"workspace_name"`
	Role          string `json:"role"`
}

// ResolvePostAuth determines the workspace state after a user authenticates.
//
// Logic:
//   - Exactly one active membership -> that workspace becomes active (Req 15.3)
//   - More than one -> most recently active becomes active, return list for selector (Req 15.4)
//   - Zero active + zero pending invitations -> needs_workspace = true (Req 15.5)
//   - Zero active + pending invitations -> return invitations + create option (Req 15.6)
//
// Requirements: 15.3, 15.4, 15.5, 15.6, 15.11
func (w *WorkspaceService) ResolvePostAuth(ctx context.Context, userID, email string) (PostAuthResult, error) {
	membershipsColl := configs.MI.DB.Collection("memberships")
	invitationsColl := configs.MI.DB.Collection("invitations")
	workspacesColl := configs.MI.DB.Collection("workspaces")

	// Step 1: Query active memberships sorted by last_active_at descending.
	// The memberships_user_status_active index covers {user_id, status, last_active_at: -1}.
	filter := bson.M{"user_id": userID, "status": string(MembershipActive)}
	opts := options.Find().SetSort(bson.D{{Key: "last_active_at", Value: -1}})

	cursor, err := membershipsColl.Find(ctx, filter, opts)
	if err != nil {
		return PostAuthResult{}, fmt.Errorf("resolve post-auth: find memberships: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var memberships []Membership
	if err := cursor.All(ctx, &memberships); err != nil {
		return PostAuthResult{}, fmt.Errorf("resolve post-auth: decode memberships: %w", err)
	}

	// Case: Exactly one active membership -> that workspace becomes active (Req 15.3).
	if len(memberships) == 1 {
		m := memberships[0]
		return PostAuthResult{
			ActiveWorkspaceID: m.WorkspaceID,
			Role:              m.Role,
			Permissions:       GrantedPermissions(m.Role),
		}, nil
	}

	// Case: More than one active membership -> most recently active (first in sorted list)
	// becomes active; return the full list for the frontend selector (Req 15.4).
	if len(memberships) > 1 {
		active := memberships[0]

		// Fetch workspace details to build WorkspaceSummary list.
		workspaceIDs := make([]string, 0, len(memberships))
		roleByWorkspace := make(map[string]Role, len(memberships))
		for _, m := range memberships {
			workspaceIDs = append(workspaceIDs, m.WorkspaceID)
			roleByWorkspace[m.WorkspaceID] = m.Role
		}

		wsCursor, err := workspacesColl.Find(ctx, bson.M{"_id": bson.M{"$in": workspaceIDs}})
		if err != nil {
			return PostAuthResult{}, fmt.Errorf("resolve post-auth: find workspaces: %w", err)
		}
		defer func() { _ = wsCursor.Close(ctx) }()

		var workspaces []Workspace
		if err := wsCursor.All(ctx, &workspaces); err != nil {
			return PostAuthResult{}, fmt.Errorf("resolve post-auth: decode workspaces: %w", err)
		}

		wsMap := make(map[string]Workspace, len(workspaces))
		for _, ws := range workspaces {
			wsMap[ws.ID] = ws
		}

		summaries := make([]WorkspaceSummary, 0, len(memberships))
		for _, m := range memberships {
			ws, ok := wsMap[m.WorkspaceID]
			if !ok {
				continue
			}
			summaries = append(summaries, WorkspaceSummary{
				ID:        ws.ID,
				Name:      ws.Name,
				Slug:      ws.Slug,
				Role:      roleByWorkspace[m.WorkspaceID],
				CreatedAt: ws.CreatedAt,
			})
		}

		return PostAuthResult{
			ActiveWorkspaceID: active.WorkspaceID,
			Role:              active.Role,
			Permissions:       GrantedPermissions(active.Role),
			Workspaces:        summaries,
		}, nil
	}

	// Case: Zero active memberships. Check for pending invitations (Req 15.5, 15.6).
	normalizedEmail := identity.NormalizeEmail(email)

	invFilter := bson.M{"email": normalizedEmail, "status": "pending"}
	invCursor, err := invitationsColl.Find(ctx, invFilter)
	if err != nil {
		return PostAuthResult{}, fmt.Errorf("resolve post-auth: find invitations: %w", err)
	}
	defer func() { _ = invCursor.Close(ctx) }()

	type invDoc struct {
		ID          string `bson:"_id"`
		WorkspaceID string `bson:"workspace_id"`
		Role        string `bson:"role"`
	}

	var invitations []invDoc
	if err := invCursor.All(ctx, &invitations); err != nil {
		return PostAuthResult{}, fmt.Errorf("resolve post-auth: decode invitations: %w", err)
	}

	// Zero active memberships + zero pending invitations -> needs_workspace (Req 15.5).
	if len(invitations) == 0 {
		return PostAuthResult{
			NeedsWorkspace: true,
		}, nil
	}

	// Zero active memberships + pending invitations -> return them alongside
	// the create-a-workspace option (Req 15.6).
	// Join with workspaces to get workspace names.
	invWorkspaceIDs := make([]string, 0, len(invitations))
	for _, inv := range invitations {
		invWorkspaceIDs = append(invWorkspaceIDs, inv.WorkspaceID)
	}

	invWsCursor, err := workspacesColl.Find(ctx, bson.M{"_id": bson.M{"$in": invWorkspaceIDs}})
	if err != nil {
		return PostAuthResult{}, fmt.Errorf("resolve post-auth: find invitation workspaces: %w", err)
	}
	defer func() { _ = invWsCursor.Close(ctx) }()

	var invWorkspaces []Workspace
	if err := invWsCursor.All(ctx, &invWorkspaces); err != nil {
		return PostAuthResult{}, fmt.Errorf("resolve post-auth: decode invitation workspaces: %w", err)
	}

	wsNameMap := make(map[string]string, len(invWorkspaces))
	for _, ws := range invWorkspaces {
		wsNameMap[ws.ID] = ws.Name
	}

	pendingViews := make([]PendingInvitationView, 0, len(invitations))
	for _, inv := range invitations {
		name := wsNameMap[inv.WorkspaceID]
		if name == "" {
			// Workspace not found — skip this invitation.
			continue
		}
		pendingViews = append(pendingViews, PendingInvitationView{
			ID:            inv.ID,
			WorkspaceName: name,
			Role:          inv.Role,
		})
	}

	return PostAuthResult{
		NeedsWorkspace:     len(pendingViews) == 0, // fallback if all workspaces missing
		PendingInvitations: pendingViews,
	}, nil
}
