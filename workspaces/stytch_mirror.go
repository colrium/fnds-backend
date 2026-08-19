package workspaces

import (
	"context"
	"log/slog"

	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
)

// MirrorRoleToStytch mirrors the Fundis role change to the Stytch Member's RBAC
// roles. A failure produces one warning and leaves the Fundis document alone
// (Req 7.15) — rolling back would trade a cosmetic mismatch for an authority mismatch.
func (m *MembershipService) MirrorRoleToStytch(ctx context.Context, membership Membership, ws Workspace) {
	if ws.StytchOrganizationID == "" || membership.StytchMemberID == "" {
		return
	}

	err := m.adapter.Do(ctx, "UpdateMemberRoles", false, func(attemptCtx context.Context) error {
		return m.adapter.Client().UpdateMemberRoles(attemptCtx,
			ws.StytchOrganizationID,
			membership.StytchMemberID,
			[]string{string(membership.Role)},
		)
	})
	if err != nil {
		slog.WarnContext(ctx, "stytch mirror: failed to update member roles",
			"workspace_id", membership.WorkspaceID,
			"membership_id", membership.ID,
			"role", string(membership.Role),
			"error", err.Error(),
		)
	}
}

// RevokeAndDeleteOnRemoval handles the Stytch-side cleanup when a membership is
// removed or suspended (Req 13.3, 17.3):
// - Revokes all sessions for the member
// - For removal: also deletes the Stytch member
// - On failure: sets stytch_member_delete_pending for retry (Req 13.4)
func (m *MembershipService) RevokeAndDeleteOnRemoval(ctx context.Context, membership Membership, ws Workspace, newStatus MembershipStatus) {
	if ws.StytchOrganizationID == "" || membership.StytchMemberID == "" {
		return
	}

	stytchCtx := stytch.ContextWithWorkspaceID(ctx, membership.WorkspaceID)

	// Revoke all sessions (Req 17.3).
	err := m.adapter.Do(stytchCtx, "RevokeMemberSessions", false, func(attemptCtx context.Context) error {
		return m.adapter.Client().RevokeMemberSessions(attemptCtx,
			ws.StytchOrganizationID,
			membership.StytchMemberID,
		)
	})
	if err != nil {
		slog.WarnContext(ctx, "stytch mirror: failed to revoke member sessions",
			"workspace_id", membership.WorkspaceID,
			"membership_id", membership.ID,
			"error", err.Error(),
		)
		// Continue — the membership check denies access anyway (Req 17.7).
	}

	// For removal: also delete the Stytch member.
	if newStatus == MembershipRemoved {
		deleteErr := m.adapter.Do(stytchCtx, "DeleteMember", true, func(attemptCtx context.Context) error {
			return m.adapter.Client().DeleteMember(attemptCtx,
				ws.StytchOrganizationID,
				membership.StytchMemberID,
			)
		})
		if deleteErr != nil {
			// Set the pending flag for the scheduler retry job (Req 13.4). If
			// this write itself fails, the retry scheduler never learns the
			// delete needs retrying and the Stytch member stays orphaned
			// indefinitely, so the failure must be logged rather than discarded.
			now := m.clock.Now()
			coll := configs.MI.DB.Collection("memberships")
			if _, updateErr := coll.UpdateOne(ctx, bson.M{"_id": membership.ID}, bson.M{
				"$set": bson.M{
					"stytch_member_delete_pending":         true,
					"stytch_member_delete_attempts":        1,
					"stytch_member_delete_first_failed_at": now,
					"updated_at":                           now,
				},
			}); updateErr != nil {
				slog.ErrorContext(ctx, "stytch mirror: failed to flag delete for retry",
					"workspace_id", membership.WorkspaceID,
					"membership_id", membership.ID,
					"error", updateErr.Error(),
				)
			}
			slog.WarnContext(ctx, "stytch mirror: delete member failed, flagged for retry",
				"workspace_id", membership.WorkspaceID,
				"membership_id", membership.ID,
				"error", deleteErr.Error(),
			)
		}
	}
}

// RevokeAllSessionsForUser revokes every Stytch session held by every linked
// member across every organization. Called when a user's status is set to
// "suspended" (Req 17.5).
func (m *MembershipService) RevokeAllSessionsForUser(ctx context.Context, userID string) {
	coll := configs.MI.DB.Collection("memberships")
	cursor, err := coll.Find(ctx, bson.M{
		"user_id":          userID,
		"stytch_member_id": bson.M{"$ne": ""},
	})
	if err != nil {
		slog.ErrorContext(ctx, "stytch mirror: failed to query memberships for session revocation",
			"user_id", userID,
			"error", err.Error(),
		)
		return
	}
	defer func() { _ = cursor.Close(ctx) }()

	wsColl := configs.MI.DB.Collection("workspaces")

	for cursor.Next(ctx) {
		var mem Membership
		if cursor.Decode(&mem) != nil {
			continue
		}

		var ws Workspace
		if wsColl.FindOne(ctx, bson.M{"_id": mem.WorkspaceID}).Decode(&ws) != nil {
			continue
		}

		if ws.StytchOrganizationID == "" {
			continue
		}

		if err := m.adapter.Do(ctx, "RevokeMemberSessions", false, func(attemptCtx context.Context) error {
			return m.adapter.Client().RevokeMemberSessions(attemptCtx,
				ws.StytchOrganizationID,
				mem.StytchMemberID,
			)
		}); err != nil {
			slog.ErrorContext(ctx, "stytch mirror: failed to revoke sessions for membership",
				"workspace_id", mem.WorkspaceID,
				"membership_id", mem.ID,
				"error", err.Error(),
			)
		}
	}
}
