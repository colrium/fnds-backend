package workspaces

import (
	"context"
	"log/slog"
	"time"

	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
)

// RetentionConfig holds the minimum retention periods for different record types.
// These are the MINIMUMS from the requirements — the actual purge only runs
// when a data-retention policy sets the MAXIMUM (which hasn't been defined yet).
//
// Until a maximum retention policy is set, this job is a no-op that only logs
// what it WOULD purge. This is Deferred Item 7 from the design.
type RetentionConfig struct {
	// InvitationMinRetention is the minimum time to keep invitations after they
	// leave "pending" status. Req 12.10 sets this at 180 days.
	InvitationMinRetention time.Duration

	// AuditMinRetention is the minimum time to keep audit entries.
	// Req 23.6 sets this at 2 years (730 days).
	AuditMinRetention time.Duration

	// PurgeEnabled controls whether actual deletions are performed.
	// When false (the default), the job only reports what it would purge.
	// This stays false until a formal data-retention policy sets the maximum.
	PurgeEnabled bool

	// MaxInvitationAge is the maximum age after which invitations are eligible for purge.
	// Only relevant when PurgeEnabled is true.
	// Must be >= InvitationMinRetention.
	MaxInvitationAge time.Duration

	// MaxAuditAge is the maximum age after which audit entries are eligible for purge.
	// Only relevant when PurgeEnabled is true.
	// Must be >= AuditMinRetention.
	MaxAuditAge time.Duration
}

// DefaultRetentionConfig returns a config with minimums set but purge disabled.
func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		InvitationMinRetention: 180 * 24 * time.Hour,     // 180 days (Req 12.10)
		AuditMinRetention:      730 * 24 * time.Hour,     // 2 years (Req 23.6)
		PurgeEnabled:           false,                    // disabled until policy set
		MaxInvitationAge:       365 * 24 * time.Hour,     // 1 year (when enabled)
		MaxAuditAge:            3 * 365 * 24 * time.Hour, // 3 years (when enabled)
	}
}

// RetentionReport records what a retention purge would or did do.
type RetentionReport struct {
	InvitationsEligible  int64 `json:"invitations_eligible"`
	InvitationsPurged    int64 `json:"invitations_purged"`
	AuditEntriesEligible int64 `json:"audit_entries_eligible"`
	AuditEntriesPurged   int64 `json:"audit_entries_purged"`
	DryRun               bool  `json:"dry_run"`
}

// RunRetentionPurge checks for records that exceed the maximum retention age
// and optionally deletes them.
//
// When PurgeEnabled is false (the default), this only counts eligible records
// and logs the result — it deletes nothing. This satisfies the design's
// position that "no purge exists" until a data-retention policy sets the maximum.
//
// Requirements: 12.10, 23.6
func RunRetentionPurge(ctx context.Context, cfg RetentionConfig, clock stytch.Clock) (RetentionReport, error) {
	now := clock.Now()
	report := RetentionReport{DryRun: !cfg.PurgeEnabled}

	// ─── Invitations ──────────────────────────────────────────────────
	// Eligible: non-pending invitations older than MaxInvitationAge.
	invCutoff := now.Add(-cfg.MaxInvitationAge)
	invColl := configs.MI.DB.Collection("invitations")

	invFilter := bson.M{
		"status":     bson.M{"$ne": "pending"},
		"updated_at": bson.M{"$lt": invCutoff},
	}

	invCount, err := invColl.CountDocuments(ctx, invFilter)
	if err != nil {
		return report, err
	}
	report.InvitationsEligible = invCount

	if cfg.PurgeEnabled && invCount > 0 {
		// Verify we're past the minimum retention.
		if cfg.MaxInvitationAge >= cfg.InvitationMinRetention {
			result, err := invColl.DeleteMany(ctx, invFilter)
			if err != nil {
				slog.ErrorContext(ctx, "retention: failed to purge invitations", "error", err)
			} else {
				report.InvitationsPurged = result.DeletedCount
			}
		}
	}

	// ─── Audit entries ────────────────────────────────────────────────
	// Eligible: entries older than MaxAuditAge.
	auditCutoff := now.Add(-cfg.MaxAuditAge)
	auditColl := configs.MI.DB.Collection("audit_log_entries")

	auditFilter := bson.M{
		"created_at": bson.M{"$lt": auditCutoff},
	}

	auditCount, err := auditColl.CountDocuments(ctx, auditFilter)
	if err != nil {
		return report, err
	}
	report.AuditEntriesEligible = auditCount

	if cfg.PurgeEnabled && auditCount > 0 {
		// Verify we're past the minimum retention.
		if cfg.MaxAuditAge >= cfg.AuditMinRetention {
			result, err := auditColl.DeleteMany(ctx, auditFilter)
			if err != nil {
				slog.ErrorContext(ctx, "retention: failed to purge audit entries", "error", err)
			} else {
				report.AuditEntriesPurged = result.DeletedCount
			}
		}
	}

	// Log the report.
	slog.InfoContext(ctx, "retention purge completed",
		"invitations_eligible", report.InvitationsEligible,
		"invitations_purged", report.InvitationsPurged,
		"audit_entries_eligible", report.AuditEntriesEligible,
		"audit_entries_purged", report.AuditEntriesPurged,
		"dry_run", report.DryRun,
	)

	return report, nil
}
