package workspaces

import (
	"context"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MigrationRun records one execution of the employer migration batch job.
// Requirements: 21.16.
type MigrationRun struct {
	ID          string     `bson:"_id" json:"id"`
	DryRun      bool       `bson:"dry_run" json:"dry_run"`
	BatchSize   int        `bson:"batch_size" json:"batch_size"`
	TotalCount  int        `bson:"total_count" json:"total_count"`
	Migrated    int        `bson:"migrated" json:"migrated"`
	Skipped     int        `bson:"skipped" json:"skipped"`
	Failed      int        `bson:"failed" json:"failed"`
	StartedAt   time.Time  `bson:"started_at" json:"started_at"`
	CompletedAt *time.Time `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
}

// MigrationFinding records the outcome of one user processed by a migration run.
// Requirements: 21.16.
type MigrationFinding struct {
	ID                   string `bson:"_id" json:"id"`
	RunID                string `bson:"run_id" json:"run_id"`
	UserID               string `bson:"user_id" json:"user_id"`
	Outcome              string `bson:"outcome" json:"outcome"`
	Reason               string `bson:"reason,omitempty" json:"reason,omitempty"`
	WorkspaceID          string `bson:"workspace_id,omitempty" json:"workspace_id,omitempty"`
	StytchOrganizationID string `bson:"stytch_organization_id,omitempty" json:"stytch_organization_id,omitempty"`
	WorkspaceName        string `bson:"workspace_name,omitempty" json:"workspace_name,omitempty"`
}

// EnsureMigrationIndexes creates the indexes for the employer_migration_runs
// and employer_migration_findings collections.
// Called once at startup from main.go. Idempotent.
// Requirements: 21.16.
func EnsureMigrationIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("employer_migration_findings")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// migration_findings_run — run + outcome for querying results per run
			Keys:    bson.D{{Key: "run_id", Value: 1}, {Key: "outcome", Value: 1}},
			Options: options.Index().SetName("migration_findings_run"),
		},
	})
	return err
}
