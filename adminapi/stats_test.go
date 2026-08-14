package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestParseStatsDaysDefaultsAndClamps(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"empty defaults to 30", "", defaultStatsDays},
		{"valid value used", "days=7", 7},
		{"zero rejected", "days=0", defaultStatsDays},
		{"negative rejected", "days=-5", defaultStatsDays},
		{"garbage rejected", "days=abc", defaultStatsDays},
		{"oversized clamped", "days=9999", maxStatsDays},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStatsDays(newTestContext(tt.query))
			if got != tt.want {
				t.Errorf("parseStatsDays() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHandleGetStatsReturnsZeroFilledSeriesOnEmptyDB(t *testing.T) {
	mt := useAdminMockMongo(t, "users")

	// Every count in this handler goes through CountDocuments/Aggregate,
	// which under mongo-driver v1.17 always runs as an aggregate pipeline,
	// so each call needs a cursor-shaped mock response rather than a plain
	// command response (confirmed empirically while building the users list
	// handler tests). Stats issues several counts (users by type, by
	// status, job requests by status, transactions by status, transaction
	// volume) plus one series aggregation. Queue enough empty-cursor
	// responses to satisfy every downstream call regardless of ordering.
	for i := 0; i < 20; i++ {
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))
	}

	ctx, rec := adminHandlerContext(http.MethodGet, "/stats", "")
	if err := HandleGetStats(ctx); err != nil {
		t.Fatalf("HandleGetStats() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data Stats `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Data.SignupSeries == nil {
		t.Error("SignupSeries is nil, want zero-filled slice")
	}
	if len(body.Data.SignupSeries) != defaultStatsDays {
		t.Errorf("SignupSeries length = %d, want %d", len(body.Data.SignupSeries), defaultStatsDays)
	}
	if body.Data.UsersByType == nil {
		t.Error("UsersByType is nil, want empty map")
	}
	if body.Data.JobRequestsByStatus == nil {
		t.Error("JobRequestsByStatus is nil, want empty map")
	}
	if body.Data.TransactionsByStatus == nil {
		t.Error("TransactionsByStatus is nil, want empty map")
	}
}

func TestBuildDailySeriesFillsGapsWithZero(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	counts := map[string]int64{
		"2026-01-01": 3,
		"2026-01-03": 5,
	}
	series := buildDailySeries(counts, start, 3)
	want := []DailyCount{
		{Date: "2026-01-01", Count: 3},
		{Date: "2026-01-02", Count: 0},
		{Date: "2026-01-03", Count: 5},
	}
	gotJSON, _ := json.Marshal(series)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("buildDailySeries() = %s, want %s", gotJSON, wantJSON)
	}
}
