package workspaces

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"backend/audit"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// newZeroOwnerTestService builds a MembershipService with no Stytch adapter:
// DetectZeroOwnerWorkspaces reads Mongo only.
func newZeroOwnerTestService() *MembershipService {
	clock := stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return NewMembershipService(nil, audit.NewLogger(), clock, false)
}

func TestDetectZeroOwnerWorkspacesReturnsOnlyWorkspacesWithoutAnActiveOwner(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("detect", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			// Aggregate over memberships: only ws-healthy has an active owner.
			mtest.CreateCursorResponse(0, "fundis.memberships", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-healthy"}, {Key: "count", Value: int32(1)}},
			),
			// Find over workspaces: three live workspaces.
			mtest.CreateCursorResponse(0, "fundis.workspaces", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-healthy"}, {Key: "name", Value: "Healthy"}},
				bson.D{{Key: "_id", Value: "ws-orphan-a"}, {Key: "name", Value: "Orphan A"}},
				bson.D{{Key: "_id", Value: "ws-orphan-b"}, {Key: "name", Value: "Orphan B"}},
			),
		)

		got, err := newZeroOwnerTestService().DetectZeroOwnerWorkspaces(context.Background())
		if err != nil {
			t.Fatalf("DetectZeroOwnerWorkspaces: %v", err)
		}

		want := []string{"ws-orphan-a", "ws-orphan-b"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})
}

func TestDetectZeroOwnerWorkspacesReturnsEmptyWhenEveryWorkspaceHasAnOwner(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("healthy", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "fundis.memberships", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-1"}, {Key: "count", Value: int32(1)}},
				bson.D{{Key: "_id", Value: "ws-2"}, {Key: "count", Value: int32(1)}},
			),
			mtest.CreateCursorResponse(0, "fundis.workspaces", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-1"}},
				bson.D{{Key: "_id", Value: "ws-2"}},
			),
		)

		got, err := newZeroOwnerTestService().DetectZeroOwnerWorkspaces(context.Background())
		if err != nil {
			t.Fatalf("DetectZeroOwnerWorkspaces: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want no zero-owner workspaces", got)
		}
	})
}

// Req 14.15: the finding must reach a human. The job emits one error-severity
// record naming every affected workspace, so a zero-owner workspace is an
// alert and not a value quietly dropped on the floor.
func TestReportZeroOwnerWorkspacesLogsErrorSeverityNamingEachWorkspace(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("alarm", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "fundis.memberships", mtest.FirstBatch),
			mtest.CreateCursorResponse(0, "fundis.workspaces", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-orphan"}},
			),
		)

		var buf bytes.Buffer
		restore := swapDefaultLogger(&buf, slog.LevelDebug)
		defer restore()

		jobs := NewSchedulerJobs(
			func(context.Context) (int64, error) { return 0, nil },
			nil,
			newZeroOwnerTestService(),
			nil,
			stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			DefaultRetentionConfig(),
		)
		jobs.ReportZeroOwnerWorkspaces()

		out := buf.String()
		if !strings.Contains(out, "ws-orphan") {
			t.Errorf("log output does not name the affected workspace: %s", out)
		}
		if !strings.Contains(out, "level=ERROR") {
			t.Errorf("zero-owner finding was not logged at error severity: %s", out)
		}
	})
}

func TestReportZeroOwnerWorkspacesStaysQuietWhenThereIsNothingToReport(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("quiet", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "fundis.memberships", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-1"}, {Key: "count", Value: int32(1)}},
			),
			mtest.CreateCursorResponse(0, "fundis.workspaces", mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "ws-1"}},
			),
		)

		var buf bytes.Buffer
		restore := swapDefaultLogger(&buf, slog.LevelDebug)
		defer restore()

		jobs := NewSchedulerJobs(
			func(context.Context) (int64, error) { return 0, nil },
			nil,
			newZeroOwnerTestService(),
			nil,
			stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			DefaultRetentionConfig(),
		)
		jobs.ReportZeroOwnerWorkspaces()

		if strings.Contains(buf.String(), "level=ERROR") {
			t.Errorf("healthy run logged an error: %s", buf.String())
		}
	})
}

func TestPurgeExpiredRecordsRunsTheConfiguredPurge(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("purge job", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		mt.AddMockResponses(
			countResponse("fundis.invitations", 2),
			countResponse("fundis.audit_log_entries", 5),
		)

		var buf bytes.Buffer
		restore := swapDefaultLogger(&buf, slog.LevelDebug)
		defer restore()

		jobs := NewSchedulerJobs(
			func(context.Context) (int64, error) { return 0, nil },
			nil,
			newZeroOwnerTestService(),
			nil,
			stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			DefaultRetentionConfig(),
		)
		jobs.PurgeExpiredRecords()

		out := buf.String()
		if !strings.Contains(out, "retention purge completed") {
			t.Errorf("purge job produced no report: %s", out)
		}
		if !strings.Contains(out, "invitations_eligible=2") {
			t.Errorf("purge job report is missing the invitation count: %s", out)
		}
	})
}

// swapDefaultLogger points slog's default logger at w for the duration of a
// test and returns the restore function.
func swapDefaultLogger(w *bytes.Buffer, level slog.Level) func() {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
	return func() { slog.SetDefault(previous) }
}
