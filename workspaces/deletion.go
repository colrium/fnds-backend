package workspaces

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// DeletionError is a structured error for deletion operations.
type DeletionError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *DeletionError) Error() string { return e.Message }

// ScheduleDeletion schedules a workspace for deletion after a 7-day grace period.
//
// Steps:
// 1. Require confirmation value equal to workspace name -> 400 if differs (Req 18.1)
// 2. Reject any role other than owner -> 403 (Req 18.2)
// 3. Check blockers:
//   - Active subscription -> 409 active_subscription (Req 18.11)
//   - Unsettled artisan obligations -> 409 outstanding_obligations (Req 18.12)
//
// 4. Set deletion_state to "scheduled" with effective_at = now + 7 days (Req 18.3)
// 5. Close every open job_vacancies document (Req 18.5)
// 6. Write audit entry (Req 18.13)
//
// Requirements: 18.1, 18.2, 18.3, 18.5, 18.6, 18.11, 18.12
func (w *WorkspaceService) ScheduleDeletion(ctx context.Context, workspaceID, confirmation, actorUserID string, actorRole Role) error {
	// Step 1: Find the workspace.
	workspacesColl := configs.MI.DB.Collection("workspaces")
	var ws Workspace
	err := workspacesColl.FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&ws)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &DeletionError{Status: 404, Code: "workspace_not_found", Message: "workspace not found"}
		}
		return fmt.Errorf("schedule deletion: find workspace: %w", err)
	}

	// Step 1b: Require confirmation value equal to workspace name (Req 18.1).
	if confirmation != ws.Name {
		return &DeletionError{
			Status: 400, Code: "confirmation_mismatch",
			Message: "confirmation must match the workspace name exactly",
		}
	}

	// Step 2: Reject any role other than owner (Req 18.2).
	if actorRole != RoleOwner {
		return &DeletionError{Status: 403, Code: "owner_required", Message: "only the owner can delete a workspace"}
	}

	// Step 3a: Check for active subscriptions (Req 18.11).
	subsColl := configs.MI.DB.Collection("user_subscriptions")
	activeSubCount, err := subsColl.CountDocuments(ctx, bson.M{
		"workspace_id": workspaceID,
		"status":       "active",
	})
	if err != nil {
		return fmt.Errorf("schedule deletion: check subscriptions: %w", err)
	}
	if activeSubCount > 0 {
		return &DeletionError{
			Status: 409, Code: "active_subscription",
			Message: "cannot delete workspace with active subscriptions; cancel them first",
		}
	}

	// Step 3b: Check for unsettled obligations (Req 18.12).
	txColl := configs.MI.DB.Collection("transactions")
	outstandingCount, err := txColl.CountDocuments(ctx, bson.M{
		"workspace_id": workspaceID,
		"status":       bson.M{"$nin": []string{"completed", "cancelled", "refunded"}},
	})
	if err != nil {
		return fmt.Errorf("schedule deletion: check obligations: %w", err)
	}
	if outstandingCount > 0 {
		return &DeletionError{
			Status: 409, Code: "outstanding_obligations",
			Message: "cannot delete workspace with unsettled payment obligations",
			Details: map[string]any{"count": outstandingCount},
		}
	}

	// Step 4: Set deletion state to "scheduled" with effective_at = now + 7 days.
	now := w.clock.Now().UTC()
	effectiveAt := now.Add(7 * 24 * time.Hour)

	_, err = workspacesColl.UpdateOne(ctx,
		bson.M{"_id": workspaceID, "deletion_state": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{
			"deletion_state":        DeletionScheduled,
			"deletion_requested_by": actorUserID,
			"deletion_effective_at": effectiveAt,
			"updated_at":            now,
		}},
	)
	if err != nil {
		return fmt.Errorf("schedule deletion: update workspace: %w", err)
	}

	// Step 5: Close every open job_vacancies document (Req 18.5).
	vacanciesColl := configs.MI.DB.Collection("job_vacancies")
	_, vacancyErr := vacanciesColl.UpdateMany(ctx,
		bson.M{"workspace_id": workspaceID, "status": "open"},
		bson.M{"$set": bson.M{"status": "closed", "updated_at": now}},
	)

	// Step 6: Write audit entry (Req 18.13). The deletion_state change in step
	// 4 has already been committed, so the audit entry is written even when
	// closing vacancies fails — otherwise a state transition would exist with
	// no trail. vacancies_closed records whether step 5 landed.
	w.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionWorkspaceDeletionState,
		TargetKind:  "workspace",
		TargetID:    workspaceID,
		Previous:    map[string]any{"deletion_state": ""},
		New: map[string]any{
			"deletion_state":   "scheduled",
			"effective_at":     effectiveAt.Format(time.RFC3339),
			"vacancies_closed": vacancyErr == nil,
		},
	})

	if vacancyErr != nil {
		slog.ErrorContext(ctx, "schedule deletion: failed to close open vacancies",
			"workspace_id", workspaceID,
			"error", vacancyErr.Error(),
		)
		return fmt.Errorf("schedule deletion: close vacancies: %w", vacancyErr)
	}

	return nil
}

// CancelDeletion clears the deletion state and restores write access.
// Does NOT reopen vacancies closed under ScheduleDeletion (Req 18.7).
//
// Requirements: 18.4, 18.7
func (w *WorkspaceService) CancelDeletion(ctx context.Context, workspaceID, actorUserID string) error {
	now := w.clock.Now().UTC()
	workspacesColl := configs.MI.DB.Collection("workspaces")

	_, err := workspacesColl.UpdateOne(ctx,
		bson.M{"_id": workspaceID, "deletion_state": string(DeletionScheduled)},
		bson.M{
			"$unset": bson.M{
				"deletion_state":        "",
				"deletion_requested_by": "",
				"deletion_effective_at": "",
			},
			"$set": bson.M{"updated_at": now},
		},
	)
	if err != nil {
		return fmt.Errorf("cancel deletion: %w", err)
	}

	// Write audit entry.
	w.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionWorkspaceDeletionState,
		TargetKind:  "workspace",
		TargetID:    workspaceID,
		Previous:    map[string]any{"deletion_state": "scheduled"},
		New:         map[string]any{"deletion_state": ""},
	})

	return nil
}

// MarkDeletedFromWebhook applies an organization.delete webhook event (Req
// 19.9). It sets the Workspace deletion state to "deleted" ONLY where the
// Fundis deletion state is already "scheduled" or "deleted" — the
// Onboarding_Service is the sole owner of *initiating* organization deletion
// during onboarding abandonment (Req 3.8), so an organization-deletion event
// for a Workspace whose Fundis state is neither "scheduled" nor "deleted" is
// unexpected and is logged rather than acted on. Returns nil in both the
// applied and the unexpected-but-logged case; the caller (the webhook
// handler) treats this as processed either way.
func (w *WorkspaceService) MarkDeletedFromWebhook(ctx context.Context, workspaceID string, eventTS time.Time) error {
	workspacesColl := configs.MI.DB.Collection("workspaces")
	now := w.clock.Now().UTC()

	res, err := workspacesColl.UpdateOne(ctx,
		bson.M{
			"_id":            workspaceID,
			"deletion_state": bson.M{"$in": []DeletionState{DeletionScheduled, DeletionDeleted}},
			"$or": bson.A{
				bson.M{"last_webhook_event_at": bson.M{"$exists": false}},
				bson.M{"last_webhook_event_at": bson.M{"$lt": eventTS}},
			},
		},
		bson.M{"$set": bson.M{
			"deletion_state":        string(DeletionDeleted),
			"last_webhook_event_at": eventTS,
			"updated_at":            now,
		}},
	)
	if err != nil {
		return fmt.Errorf("mark deleted from webhook: %w", err)
	}
	if res.MatchedCount == 0 {
		slog.ErrorContext(ctx, "webhook: organization.delete for workspace with unexpected deletion state",
			"workspace_id", workspaceID,
		)
		return nil
	}

	w.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorKind:   "system",
		Action:      audit.ActionWorkspaceDeletionState,
		TargetKind:  "workspace",
		TargetID:    workspaceID,
		New:         map[string]any{"deletion_state": "deleted"},
	})
	return nil
}

// EffectDueDeletions processes all workspaces whose scheduled deletion has passed.
// Called by the hourly scheduler job.
//
// For each due workspace:
// 1. Set deletion_state to "deleted" (Req 18.8)
// 2. Set every membership to status "removed" (Req 18.8)
// 3. Delete the Stytch organization, treating KindNotFound as success (Req 18.8)
// 4. Retain all financial/application documents (Req 18.9)
// 5. Write audit entry per deletion state change (Req 18.13)
//
// Re-running converges — idempotent with no transaction.
// Requirements: 18.8, 18.9, 18.10, 18.13, 18.14
func (w *WorkspaceService) EffectDueDeletions(ctx context.Context, now time.Time) (int, error) {
	workspacesColl := configs.MI.DB.Collection("workspaces")
	membershipsColl := configs.MI.DB.Collection("memberships")

	// Step 1: Query workspaces where deletion_state == "scheduled" and
	// deletion_effective_at <= now. Uses the workspaces_deletion_due index.
	filter := bson.M{
		"deletion_state":        string(DeletionScheduled),
		"deletion_effective_at": bson.M{"$lte": now},
	}

	cursor, err := workspacesColl.Find(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("effect due deletions: find scheduled: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var dueWorkspaces []Workspace
	if err := cursor.All(ctx, &dueWorkspaces); err != nil {
		return 0, fmt.Errorf("effect due deletions: decode workspaces: %w", err)
	}

	effected := 0

	for _, ws := range dueWorkspaces {
		// Step 2a: UpdateMany memberships: set status to "removed", $inc
		// authority_version. Memberships are updated BEFORE the workspace so
		// that a failure here leaves deletion_state at "scheduled" — the next
		// run's filter (deletion_state == "scheduled") still matches this
		// workspace and retries both steps. Doing it in the other order (as an
		// earlier version of this function did) let a membership-update
		// failure strand the workspace in "deleted" with active memberships
		// forever, because the workspace filter would never match it again.
		_, err := membershipsColl.UpdateMany(ctx,
			bson.M{"workspace_id": ws.ID, "status": bson.M{"$ne": string(MembershipRemoved)}},
			bson.M{
				"$set": bson.M{
					"status":     string(MembershipRemoved),
					"updated_at": now,
				},
				"$inc": bson.M{"authority_version": 1},
			},
		)
		if err != nil {
			slog.ErrorContext(ctx, "effect due deletions: remove memberships",
				"workspace_id", ws.ID,
				"error", err.Error(),
			)
			continue
		}

		// Step 2b: Update workspace: deletion_state = "deleted".
		// The filter includes deletion_state == "scheduled" so re-running is idempotent.
		res, err := workspacesColl.UpdateOne(ctx,
			bson.M{"_id": ws.ID, "deletion_state": string(DeletionScheduled)},
			bson.M{"$set": bson.M{
				"deletion_state": string(DeletionDeleted),
				"updated_at":     now,
			}},
		)
		if err != nil {
			slog.ErrorContext(ctx, "effect due deletions: update workspace state",
				"workspace_id", ws.ID,
				"error", err.Error(),
			)
			continue
		}
		// If no document matched, this workspace was already effected (idempotent).
		if res.MatchedCount == 0 {
			continue
		}

		// Step 2c: Delete the Stytch organization if one exists.
		if ws.StytchOrganizationID != "" {
			stytchCtx := stytch.ContextWithWorkspaceID(ctx, ws.ID)
			deleteErr := w.adapter.Do(stytchCtx, "DeleteOrganization", true, func(attemptCtx context.Context) error {
				return w.adapter.Client().DeleteOrganization(attemptCtx, ws.StytchOrganizationID)
			})
			if deleteErr != nil {
				// Treat KindNotFound as success — the org may already have been
				// deleted by a webhook or a prior run.
				var stytchErr *stytch.Error
				if errors.As(deleteErr, &stytchErr) && stytchErr.Kind == stytch.KindNotFound {
					// Not found is success — already deleted.
				} else {
					slog.ErrorContext(ctx, "effect due deletions: delete stytch organization",
						"workspace_id", ws.ID,
						"stytch_org_id", ws.StytchOrganizationID,
						"error", deleteErr.Error(),
					)
					// Continue processing — the deletion state is already "deleted"
					// and a retry job can clean up the Stytch side later.
				}
			}
		}

		// Step 2d: Write audit entry (Req 18.13).
		w.audit.Write(ctx, audit.WriteInput{
			WorkspaceID: ws.ID,
			ActorUserID: ws.DeletionRequestedBy,
			ActorKind:   "system",
			Action:      audit.ActionWorkspaceDeletionState,
			TargetKind:  "workspace",
			TargetID:    ws.ID,
			Previous:    map[string]any{"deletion_state": "scheduled"},
			New:         map[string]any{"deletion_state": "deleted"},
		})

		effected++
	}

	return effected, nil
}
