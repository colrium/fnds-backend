package workspaces

import (
	"context"
	"os"
	"testing"
	"time"

	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// useRetentionTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup. Mirrors useResolverTestMongo.
func useRetentionTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// countResponse builds the cursor response CountDocuments reads: the driver
// runs an aggregate whose single document carries the count under "n".
func countResponse(ns string, n int32) bson.D {
	return mtest.CreateCursorResponse(0, ns, mtest.FirstBatch, bson.D{{Key: "n", Value: n}})
}

func TestRunRetentionPurgeDryRunCountsWithoutDeleting(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("dry run", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		// Only two responses are queued: the two CountDocuments calls. A
		// DeleteMany would consume a third and fail the test, which is exactly
		// the assertion — a dry run must issue no delete.
		mt.AddMockResponses(
			countResponse("fundis.invitations", 3),
			countResponse("fundis.audit_log_entries", 7),
		)

		cfg := DefaultRetentionConfig()
		if cfg.PurgeEnabled {
			t.Fatal("DefaultRetentionConfig must have PurgeEnabled false")
		}

		clock := stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		report, err := RunRetentionPurge(context.Background(), cfg, clock)
		if err != nil {
			t.Fatalf("RunRetentionPurge: %v", err)
		}

		if !report.DryRun {
			t.Error("report.DryRun = false, want true when PurgeEnabled is false")
		}
		if report.InvitationsEligible != 3 {
			t.Errorf("InvitationsEligible = %d, want 3", report.InvitationsEligible)
		}
		if report.AuditEntriesEligible != 7 {
			t.Errorf("AuditEntriesEligible = %d, want 7", report.AuditEntriesEligible)
		}
		if report.InvitationsPurged != 0 || report.AuditEntriesPurged != 0 {
			t.Errorf("dry run purged records: invitations=%d audit=%d",
				report.InvitationsPurged, report.AuditEntriesPurged)
		}
	})
}

func TestRunRetentionPurgeDeletesWhenEnabled(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("purge enabled", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			countResponse("fundis.invitations", 3),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: int32(3)}),
			countResponse("fundis.audit_log_entries", 7),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: int32(7)}),
		)

		cfg := DefaultRetentionConfig()
		cfg.PurgeEnabled = true

		clock := stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		report, err := RunRetentionPurge(context.Background(), cfg, clock)
		if err != nil {
			t.Fatalf("RunRetentionPurge: %v", err)
		}

		if report.DryRun {
			t.Error("report.DryRun = true, want false when PurgeEnabled is true")
		}
		if report.InvitationsPurged != 3 {
			t.Errorf("InvitationsPurged = %d, want 3", report.InvitationsPurged)
		}
		if report.AuditEntriesPurged != 7 {
			t.Errorf("AuditEntriesPurged = %d, want 7", report.AuditEntriesPurged)
		}
	})
}

// A max age below the statutory minimum must never delete, even with
// PurgeEnabled true: Req 12.10 (180 days) and Req 23.6 (2 years) are floors.
// Only two responses are queued, so any DeleteMany fails the test.
func TestRunRetentionPurgeRefusesToDeleteBelowMinimumRetention(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("below minimum", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			countResponse("fundis.invitations", 4),
			countResponse("fundis.audit_log_entries", 9),
		)

		cfg := DefaultRetentionConfig()
		cfg.PurgeEnabled = true
		cfg.MaxInvitationAge = 30 * 24 * time.Hour // below the 180-day minimum
		cfg.MaxAuditAge = 90 * 24 * time.Hour      // below the 2-year minimum

		clock := stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		report, err := RunRetentionPurge(context.Background(), cfg, clock)
		if err != nil {
			t.Fatalf("RunRetentionPurge: %v", err)
		}

		if report.InvitationsPurged != 0 || report.AuditEntriesPurged != 0 {
			t.Errorf("purged below the retention minimum: invitations=%d audit=%d",
				report.InvitationsPurged, report.AuditEntriesPurged)
		}
	})
}

func TestRetentionConfigFromEnvDefaultsToTheDeferredNoOp(t *testing.T) {
	clearRetentionEnv(t)

	cfg, err := RetentionConfigFromEnv()
	if err != nil {
		t.Fatalf("RetentionConfigFromEnv: %v", err)
	}
	if cfg != DefaultRetentionConfig() {
		t.Errorf("config with no env set = %+v, want DefaultRetentionConfig() = %+v",
			cfg, DefaultRetentionConfig())
	}
}

func TestRetentionConfigFromEnvAppliesOverrides(t *testing.T) {
	clearRetentionEnv(t)
	t.Setenv("WORKSPACE_RETENTION_PURGE_ENABLED", "true")
	t.Setenv("WORKSPACE_RETENTION_MAX_INVITATION_AGE", "8760h") // 1 year
	t.Setenv("WORKSPACE_RETENTION_MAX_AUDIT_AGE", "43800h")     // 5 years

	cfg, err := RetentionConfigFromEnv()
	if err != nil {
		t.Fatalf("RetentionConfigFromEnv: %v", err)
	}
	if !cfg.PurgeEnabled {
		t.Error("PurgeEnabled = false, want true")
	}
	if cfg.MaxInvitationAge != 8760*time.Hour {
		t.Errorf("MaxInvitationAge = %s, want 8760h", cfg.MaxInvitationAge)
	}
	if cfg.MaxAuditAge != 43800*time.Hour {
		t.Errorf("MaxAuditAge = %s, want 43800h", cfg.MaxAuditAge)
	}
	// The minimums are not configurable — they come from the requirements.
	if cfg.InvitationMinRetention != DefaultRetentionConfig().InvitationMinRetention {
		t.Error("InvitationMinRetention must not be configurable")
	}
	if cfg.AuditMinRetention != DefaultRetentionConfig().AuditMinRetention {
		t.Error("AuditMinRetention must not be configurable")
	}
}

func TestRetentionConfigFromEnvRejectsAgeBelowStatutoryMinimum(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"invitation below 180 days", "WORKSPACE_RETENTION_MAX_INVITATION_AGE", "720h"},
		{"audit below 2 years", "WORKSPACE_RETENTION_MAX_AUDIT_AGE", "8760h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRetentionEnv(t)
			t.Setenv(tc.key, tc.value)

			if _, err := RetentionConfigFromEnv(); err == nil {
				t.Fatalf("%s=%s was accepted; it is below the requirement's minimum retention",
					tc.key, tc.value)
			}
		})
	}
}

// clearRetentionEnv unsets the retention variables for the duration of a test,
// so a value in the developer's environment cannot change the outcome.
func clearRetentionEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WORKSPACE_RETENTION_PURGE_ENABLED",
		"WORKSPACE_RETENTION_MAX_INVITATION_AGE",
		"WORKSPACE_RETENTION_MAX_AUDIT_AGE",
	} {
		if _, ok := os.LookupEnv(key); ok {
			t.Setenv(key, "")
		}
	}
}
