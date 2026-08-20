package workspaces

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
)

// SweepExpiredFunc is the signature for sweeping expired invitations.
// Injected from the invitations package to avoid a circular import.
type SweepExpiredFunc func(ctx context.Context) (int64, error)

// SchedulerJobs registers all workspace-related background jobs
// on the provided scheduler. Called from main.go after constructing the services.
//
// Hourly jobs:
//   - Invitation expiry sweep (Req 12.8)
//   - Abandoned-onboarding reaper (Req 3.7, 3.8)
//   - Workspace deletion effector (Req 18.8)
//   - Zero-owner workspace detection (Req 14.15)
//
// Frequent (15-min) jobs:
//   - Stytch member-delete retry (Req 13.4)
//
// Daily jobs:
//   - Retention purge / report (Req 12.10, 23.6)
type SchedulerJobs struct {
	sweepExpired SweepExpiredFunc
	workspaces   *WorkspaceService
	memberships  *MembershipService
	adapter      *stytch.Adapter
	clock        stytch.Clock
	retention    RetentionConfig
}

func NewSchedulerJobs(
	sweepExpired SweepExpiredFunc,
	ws *WorkspaceService,
	ms *MembershipService,
	adapter *stytch.Adapter,
	clock stytch.Clock,
	retention RetentionConfig,
) *SchedulerJobs {
	return &SchedulerJobs{
		sweepExpired: sweepExpired,
		workspaces:   ws,
		memberships:  ms,
		adapter:      adapter,
		clock:        clock,
		retention:    retention,
	}
}

// SweepExpiredInvitations is the hourly job for Req 12.8.
func (j *SchedulerJobs) SweepExpiredInvitations() {
	ctx := context.Background()
	count, err := j.sweepExpired(ctx)
	if err != nil {
		slog.Error("scheduler: sweep expired invitations failed", "error", err)
		return
	}
	if count > 0 {
		slog.Info("scheduler: swept expired invitations", "count", count)
	}
}

// PurgeExpiredRecords is the daily job for Req 12.10 and 23.6.
//
// Daily, not hourly: both retention thresholds are expressed in days and years
// (180 days for an invitation that has left "pending", 2 years for an audit
// entry), so the smallest unit of change the requirements care about is a day.
// Running the two count queries every hour would produce 24 identical reports
// for one day's worth of newly-eligible records.
//
// With PurgeEnabled false — the default, because the design defers the purge
// until a data-retention policy sets the maximum — this only reports what it
// would delete.
func (j *SchedulerJobs) PurgeExpiredRecords() {
	ctx := context.Background()
	if _, err := RunRetentionPurge(ctx, j.retention, j.clock); err != nil {
		slog.ErrorContext(ctx, "scheduler: retention purge failed", "error", err)
	}
}

// ReportZeroOwnerWorkspaces is the hourly job for Req 14.15.
//
// A workspace with zero active owner memberships is a workspace nobody can
// administer, delete, or transfer. The partial unique index makes it
// unreachable, so a non-empty result is a data-integrity alarm: one
// error-severity record naming every affected workspace, hourly, so on-call
// sees it within the hour rather than at the next daily report.
func (j *SchedulerJobs) ReportZeroOwnerWorkspaces() {
	ctx := context.Background()
	orphaned, err := j.memberships.DetectZeroOwnerWorkspaces(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "scheduler: zero-owner workspace detection failed", "error", err)
		return
	}
	if len(orphaned) == 0 {
		return
	}
	slog.ErrorContext(ctx, "scheduler: workspaces have zero active owner memberships",
		"count", len(orphaned),
		"workspace_ids", strings.Join(orphaned, ","),
	)
}

// EffectDueDeletions is the hourly job for Req 18.8.
func (j *SchedulerJobs) EffectDueDeletions() {
	ctx := context.Background()
	now := j.clock.Now()
	count, err := j.workspaces.EffectDueDeletions(ctx, now)
	if err != nil {
		slog.Error("scheduler: effect due deletions failed", "error", err)
		return
	}
	if count > 0 {
		slog.Info("scheduler: effected workspace deletions", "count", count)
	}
}

// ReapAbandonedOnboarding is the hourly job for Req 3.7, 3.8.
// Finds onboarding_states that have remained outside "complete" for >72h and
// cleans up any orphaned Stytch organizations.
func (j *SchedulerJobs) ReapAbandonedOnboarding() {
	ctx := context.Background()
	now := j.clock.Now()
	cutoff := now.Add(-72 * time.Hour)

	coll := configs.MI.DB.Collection("onboarding_states")
	cursor, err := coll.Find(ctx, bson.M{
		"stage":             bson.M{"$ne": "complete"},
		"updated_at":        bson.M{"$lt": cutoff},
		"abandoned_handled": bson.M{"$ne": true},
	})
	if err != nil {
		slog.Error("scheduler: reap abandoned onboarding query failed", "error", err)
		return
	}
	defer func() { _ = cursor.Close(ctx) }()

	var reaped int
	for cursor.Next(ctx) {
		var state struct {
			ID                   string `bson:"_id"`
			StytchOrganizationID string `bson:"stytch_organization_id"`
		}
		if err := cursor.Decode(&state); err != nil {
			continue
		}

		// If has a stytch org, check if a workspace references it.
		if state.StytchOrganizationID != "" {
			wsColl := configs.MI.DB.Collection("workspaces")
			count, _ := wsColl.CountDocuments(ctx, bson.M{"stytch_organization_id": state.StytchOrganizationID})
			if count == 0 {
				// No workspace references it — safe to delete the org.
				_ = j.adapter.Do(ctx, "DeleteOrganization", true, func(attemptCtx context.Context) error {
					return j.adapter.Client().DeleteOrganization(attemptCtx, state.StytchOrganizationID)
				})
			}
		}

		// Mark as handled.
		_, _ = coll.UpdateOne(ctx, bson.M{"_id": state.ID}, bson.M{
			"$set": bson.M{"abandoned_handled": true, "updated_at": now},
		})
		reaped++
	}

	if reaped > 0 {
		slog.Info("scheduler: reaped abandoned onboarding states", "count", reaped)
	}
}

// RetryPendingStytchDeletes is the 15-minute job for Req 13.4.
// Retries Stytch member deletions that failed, for up to 24 hours.
func (j *SchedulerJobs) RetryPendingStytchDeletes() {
	ctx := context.Background()
	now := j.clock.Now()
	cutoff := now.Add(-24 * time.Hour) // stop retrying after 24h

	coll := configs.MI.DB.Collection("memberships")
	cursor, err := coll.Find(ctx, bson.M{
		"stytch_member_delete_pending":         true,
		"stytch_member_delete_first_failed_at": bson.M{"$gt": cutoff},
	})
	if err != nil {
		slog.Error("scheduler: retry stytch deletes query failed", "error", err)
		return
	}
	defer func() { _ = cursor.Close(ctx) }()

	var retried int
	for cursor.Next(ctx) {
		var mem Membership
		if err := cursor.Decode(&mem); err != nil {
			continue
		}

		// Look up the workspace to get the stytch org ID.
		wsColl := configs.MI.DB.Collection("workspaces")
		var ws Workspace
		if wsColl.FindOne(ctx, bson.M{"_id": mem.WorkspaceID}).Decode(&ws) != nil {
			continue
		}

		// Attempt to delete the Stytch member.
		deleteErr := j.adapter.Do(ctx, "DeleteMember", true, func(attemptCtx context.Context) error {
			return j.adapter.Client().DeleteMember(attemptCtx, ws.StytchOrganizationID, mem.StytchMemberID)
		})

		if deleteErr == nil || stytch.IsNotFound(deleteErr) {
			// Success or already deleted — clear the pending flag.
			_, _ = coll.UpdateOne(ctx, bson.M{"_id": mem.ID}, bson.M{
				"$set": bson.M{"updated_at": now},
				"$unset": bson.M{
					"stytch_member_delete_pending":         "",
					"stytch_member_delete_attempts":        "",
					"stytch_member_delete_first_failed_at": "",
				},
			})
			retried++
		} else {
			// Still failing — increment attempt count.
			_, _ = coll.UpdateOne(ctx, bson.M{"_id": mem.ID}, bson.M{
				"$inc": bson.M{"stytch_member_delete_attempts": 1},
				"$set": bson.M{"updated_at": now},
			})
		}
	}

	if retried > 0 {
		slog.Info("scheduler: retried pending stytch deletes", "succeeded", retried)
	}
}
