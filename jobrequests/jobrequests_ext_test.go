package jobrequests

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestBuildJobRequestFilter(t *testing.T) {
	// Pending = open pool: only truly-unassigned requests (artisan_id == "").
	pending := buildJobRequestFilter("pending", "artisan1")
	if pending["status"] != "pending" || pending["artisan_id"] != "" {
		t.Fatalf("pending filter should be status=pending + artisan_id empty, got %v", pending)
	}
	accepted := buildJobRequestFilter("accepted", "artisan1")
	if accepted["status"] != "accepted" || accepted["artisan_id"] != "artisan1" {
		t.Fatalf("accepted filter should scope status+artisan, got %v", accepted)
	}
	completed := buildJobRequestFilter("completed", "artisan1")
	if completed["status"] != "completed" || completed["artisan_id"] != "artisan1" {
		t.Fatalf("completed filter wrong: %v", completed)
	}
	// Empty/unknown status must NOT collapse tenancy — always scope to the artisan.
	empty := buildJobRequestFilter("", "artisan1")
	if empty["artisan_id"] != "artisan1" {
		t.Fatalf("empty status must scope to artisan, got %v", empty)
	}
	if _, hasStatus := empty["status"]; hasStatus {
		t.Fatalf("empty status should not set a status filter, got %v", empty["status"])
	}
}

func TestHandleAcceptJobRequest_RequiresAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/job-requests/x/accept", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	_ = HandleAcceptJobRequest(ctx)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}
}

func TestHandleCompleteJobRequest_RequiresAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/job-requests/x/complete", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	_ = HandleCompleteJobRequest(ctx)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}
}
