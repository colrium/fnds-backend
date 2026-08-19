package workspaces

import (
	"context"
	"fmt"

	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// SwitchInput carries the parameters for switching the active workspace.
type SwitchInput struct {
	UserID            string
	TargetWorkspaceID string
	SessionToken      string // the current opaque session token
}

// SwitchResult is the response from SwitchWorkspace.
type SwitchResult struct {
	WorkspaceID string       `json:"workspace_id"`
	Role        Role         `json:"role"`
	Permissions []Permission `json:"permissions"`
	SessionJWT  string       `json:"session_jwt,omitempty"`
}

// SwitchWorkspace switches the user's active workspace.
//
// Steps:
//  1. Look for an active membership in the target workspace for this user
//  2. If none found -> 404, active workspace unchanged (Req 15.8)
//  3. Call Stytch ExchangeSession -> new session scoped to target org (Req 15.9)
//  4. If Stytch unavailable -> 503 identity_provider_unavailable (Req 20.4)
//  5. Update memberships[target].last_active_at and stytch_member_id
//  6. Return workspace, role, permissions, new session JWT (Req 15.7)
//
// The active workspace is NOT server-side session state — it is the
// organization_id inside the verified credential, and last_active_at exists
// solely to answer "which workspace at next sign-in" (Req 15.4).
func (w *WorkspaceService) SwitchWorkspace(ctx context.Context, in SwitchInput) (SwitchResult, error) {
	membershipsColl := configs.MI.DB.Collection("memberships")
	workspacesColl := configs.MI.DB.Collection("workspaces")

	// Step 1: Query memberships for an active membership in the target workspace.
	var membership Membership
	err := membershipsColl.FindOne(ctx, bson.M{
		"user_id":      in.UserID,
		"workspace_id": in.TargetWorkspaceID,
		"status":       MembershipActive,
	}).Decode(&membership)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Step 2: No active membership -> 404 (Req 15.8).
			return SwitchResult{}, &MembershipError{
				Status:  404,
				Code:    "membership_not_found",
				Message: "no active membership in target workspace",
			}
		}
		return SwitchResult{}, fmt.Errorf("switch workspace: find membership: %w", err)
	}

	// Step 3: Look up the workspace to get its StytchOrganizationID.
	var ws Workspace
	err = workspacesColl.FindOne(ctx, bson.M{"_id": in.TargetWorkspaceID}).Decode(&ws)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return SwitchResult{}, &MembershipError{
				Status:  404,
				Code:    "workspace_not_found",
				Message: "target workspace not found",
			}
		}
		return SwitchResult{}, fmt.Errorf("switch workspace: find workspace: %w", err)
	}

	// Step 4: Call Stytch ExchangeSession to get a session scoped to the target org (Req 15.9).
	var exchangedSession stytch.Session
	err = w.adapter.Do(ctx, "ExchangeSession", false, func(attemptCtx context.Context) error {
		var callErr error
		exchangedSession, callErr = w.adapter.Client().ExchangeSession(attemptCtx, in.SessionToken, ws.StytchOrganizationID)
		return callErr
	})
	if err != nil {
		// Step 4b: If Stytch unavailable -> 503 identity_provider_unavailable (Req 20.4).
		if stytch.IsUnavailable(err) {
			return SwitchResult{}, &MembershipError{
				Status:  503,
				Code:    "identity_provider_unavailable",
				Message: "identity provider is temporarily unavailable",
			}
		}
		return SwitchResult{}, fmt.Errorf("switch workspace: exchange session: %w", err)
	}

	// Step 5: Update membership's last_active_at and stytch_member_id with the
	// new member_id from the exchanged session.
	now := w.clock.Now().UTC()
	_, err = membershipsColl.UpdateOne(ctx,
		bson.M{"_id": membership.ID},
		bson.M{
			"$set": bson.M{
				"last_active_at":   now,
				"stytch_member_id": exchangedSession.MemberID,
				"updated_at":       now,
			},
		},
	)
	if err != nil {
		return SwitchResult{}, fmt.Errorf("switch workspace: update membership: %w", err)
	}

	// Step 6: Return SwitchResult with workspace ID, role, granted permissions,
	// and the new session JWT (Req 15.7).
	return SwitchResult{
		WorkspaceID: in.TargetWorkspaceID,
		Role:        membership.Role,
		Permissions: GrantedPermissions(membership.Role),
		SessionJWT:  exchangedSession.SessionJWT,
	}, nil
}
