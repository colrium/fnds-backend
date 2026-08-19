package workspaces

import (
	"context"
	"log/slog"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
)

// SetApplicationWorkspaceID sets workspace_id on a job application document
// by copying it from the target vacancy. Called by HandleCreateJobApplication
// after the application is created (Req 9.9). Returns the update error so the
// caller can decide whether to retry, fail the application creation, or
// alert — without it, a failed scoping write is invisible until a
// workspace-scoped query silently returns an incomplete result.
func SetApplicationWorkspaceID(ctx context.Context, applicationID, vacancyID string) error {
	vacanciesColl := configs.MI.DB.Collection("job_vacancies")
	applicationsColl := configs.MI.DB.Collection("job_applications")

	// Look up the vacancy to get its workspace_id.
	var vacancy struct {
		WorkspaceID string `bson:"workspace_id"`
	}
	err := vacanciesColl.FindOne(ctx, bson.M{"_id": vacancyID}).Decode(&vacancy)
	if err != nil || vacancy.WorkspaceID == "" {
		// Vacancy has no workspace_id (legacy) — leave application unscoped.
		return nil
	}

	// Set workspace_id on the application.
	if _, err := applicationsColl.UpdateOne(ctx,
		bson.M{"_id": applicationID},
		bson.M{"$set": bson.M{"workspace_id": vacancy.WorkspaceID}},
	); err != nil {
		slog.ErrorContext(ctx, "failed to set workspace_id on application",
			"application_id", applicationID,
			"vacancy_id", vacancyID,
			"error", err.Error(),
		)
		return err
	}
	return nil
}

// GetApplicationNotifyMembers returns the user IDs of all workspace members
// who hold the application.view permission for the given workspace.
// Used to notify workspace members when an application arrives (Req 22.9).
//
// If workspaceID is empty (legacy vacancy), returns nil and the caller should
// fall back to notifying the single vacancy owner.
func GetApplicationNotifyMembers(ctx context.Context, workspaceID string) []string {
	if workspaceID == "" {
		return nil
	}

	membershipsColl := configs.MI.DB.Collection("memberships")

	// Roles that hold application.view, derived from the permission matrix
	// rather than hardcoded so this query cannot go stale when the matrix changes.
	roles := RolesWithPermission(PermApplicationView)
	cursor, err := membershipsColl.Find(ctx, bson.M{
		"workspace_id": workspaceID,
		"status":       "active",
		"role":         bson.M{"$in": roles},
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to query members for application notification",
			"workspace_id", workspaceID,
			"error", err.Error(),
		)
		return nil
	}
	defer func() { _ = cursor.Close(ctx) }()

	var userIDs []string
	for cursor.Next(ctx) {
		var m struct {
			UserID string `bson:"user_id"`
		}
		if err := cursor.Decode(&m); err != nil {
			slog.ErrorContext(ctx, "failed to decode member for application notification", "error", err.Error())
			continue
		}
		userIDs = append(userIDs, m.UserID)
	}
	if err := cursor.Err(); err != nil {
		slog.ErrorContext(ctx, "cursor iteration failed for application notification",
			"workspace_id", workspaceID,
			"error", err.Error(),
		)
	}
	return userIDs
}
