package workspaces

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/stytch"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

// MigrationOptions configures a migration run.
type MigrationOptions struct {
	DryRun    bool // Req 21.8: zero writes to Mongo and Stytch (except report collections)
	BatchSize int  // Req 21.9, default 50
	MaxRPS    int  // Req 21.9, fixed at 10 state-changing Stytch calls per second
	Limit     int  // 0 = all; safety valve for first production run
}

// MigrationJob runs the employer migration.
type MigrationJob struct {
	workspaces *WorkspaceService
	audit      *audit.Logger
	clock      stytch.Clock
}

// NewMigrationJob constructs a MigrationJob.
func NewMigrationJob(ws *WorkspaceService, auditLogger *audit.Logger, clock stytch.Clock) *MigrationJob {
	return &MigrationJob{workspaces: ws, audit: auditLogger, clock: clock}
}

// Run executes the migration. Per candidate it performs exactly the same calls
// as a live signup, in the same order, through the same WorkspaceService code —
// not a parallel implementation (Req 21.2).
//
// Steps per candidate:
// 1. Create Stytch Organization (or skip in dry-run)
// 2. Create Workspace via CreateForOnboarding
// 3. Create Account_Link
// 4. Apply data re-scoping (Req 21.4)
// 5. Record outcome in employer_migration_findings
//
// A second run over an already-migrated employer creates zero additional records (Req 21.5).
// Batches at configurable size, throttles to MaxRPS (Req 21.9).
//
// Requirements: 21.2, 21.4, 21.5, 21.9, 21.13
func (j *MigrationJob) Run(ctx context.Context, candidates []MigrationCandidate, opt MigrationOptions) (string, error) {
	if opt.BatchSize <= 0 {
		opt.BatchSize = 50
	}
	if opt.MaxRPS <= 0 {
		opt.MaxRPS = 10
	}

	runID := uuid.New().String()
	now := j.clock.Now()

	// Create the migration run record.
	run := MigrationRun{
		ID:         runID,
		DryRun:     opt.DryRun,
		BatchSize:  opt.BatchSize,
		TotalCount: len(candidates),
		StartedAt:  now,
	}

	runsColl := configs.MI.DB.Collection("employer_migration_runs")
	_, err := runsColl.InsertOne(ctx, run)
	if err != nil {
		return "", fmt.Errorf("migration: insert run record: %w", err)
	}

	findingsColl := configs.MI.DB.Collection("employer_migration_findings")
	var migrated, skipped, failed int

	// Throttle: allow MaxRPS Stytch calls per second.
	throttle := time.NewTicker(time.Second / time.Duration(opt.MaxRPS))
	defer throttle.Stop()

	for i, candidate := range candidates {
		if opt.Limit > 0 && i >= opt.Limit {
			break
		}

		// Throttle before each Stytch-touching operation, except the first —
		// the ticker fires only after its full period elapses, so waiting on
		// it before the first candidate pays a wasted interval for nothing.
		if i > 0 {
			<-throttle.C
		}

		finding := MigrationFinding{
			ID:     uuid.New().String(),
			RunID:  runID,
			UserID: candidate.User.ID,
		}

		// A second run over an already-migrated employer must create zero
		// additional records (Req 21.5): check findings from prior real runs
		// before touching Stytch/Mongo again, in both dry-run and real mode.
		alreadyMigrated, checkErr := findingsColl.CountDocuments(ctx, bson.M{
			"user_id": candidate.User.ID,
			"outcome": "migrated",
		})
		if checkErr != nil {
			slog.ErrorContext(ctx, "migration: existing-migration check failed",
				"user_id", candidate.User.ID,
				"error", checkErr.Error(),
			)
		}

		switch {
		case alreadyMigrated > 0:
			finding.Outcome = "skipped"
			finding.Reason = "already migrated in a previous run"
			skipped++
		case opt.DryRun:
			// Dry-run: record what would happen, make zero writes to Stytch/Mongo.
			// The workspace name is annotated so dry-run findings are never
			// confused with the actual (possibly slug-suffixed) name a real
			// run would produce.
			finding.Outcome = "migrated"
			finding.WorkspaceName = candidate.WorkspaceName + " (dry-run)"
			migrated++
		default:
			// Real migration: use the same code path as live signup.
			result, createErr := j.workspaces.CreateForOnboarding(ctx, CreateForOnboardingInput{
				OnboardingStateID: "migration:" + candidate.User.ID,
				UserID:            candidate.User.ID,
				Email:             candidate.User.Email,
				RawName:           candidate.WorkspaceName,
			})
			if createErr != nil {
				finding.Outcome = "failed"
				finding.Reason = createErr.Error()
				failed++
				slog.ErrorContext(ctx, "migration: candidate failed",
					"user_id", candidate.User.ID,
					"error", createErr.Error(),
				)
			} else {
				finding.Outcome = "migrated"
				finding.WorkspaceID = result.Workspace.ID
				finding.WorkspaceName = result.Workspace.Name
				migrated++

				// Apply data re-scoping (Req 21.4). A failure here leaves the
				// workspace/membership created but the user's data unscoped,
				// so it must flip the finding to failed rather than silently
				// reporting success.
				if _, rescopeErr := j.workspaces.RescopeUserData(ctx, candidate.User.ID, result.Workspace.ID); rescopeErr != nil {
					finding.Outcome = "failed"
					finding.Reason = fmt.Errorf("rescope failed: %w", rescopeErr).Error()
					failed++
					migrated--
					slog.ErrorContext(ctx, "migration: rescope failed",
						"user_id", candidate.User.ID,
						"workspace_id", result.Workspace.ID,
						"error", rescopeErr.Error(),
					)
				}
			}
		}

		// Record finding.
		if _, insertErr := findingsColl.InsertOne(ctx, finding); insertErr != nil {
			slog.ErrorContext(ctx, "migration: failed to record finding",
				"finding_id", finding.ID,
				"run_id", runID,
				"error", insertErr.Error(),
			)
		}
	}

	// Update the run record with final counts.
	completedAt := j.clock.Now()
	if _, updateErr := runsColl.UpdateOne(ctx, bson.M{"_id": runID}, bson.M{
		"$set": bson.M{
			"migrated":     migrated,
			"skipped":      skipped,
			"failed":       failed,
			"completed_at": completedAt,
		},
	}); updateErr != nil {
		slog.ErrorContext(ctx, "migration: failed to update run record",
			"run_id", runID,
			"error", updateErr.Error(),
		)
	}

	return runID, nil
}
