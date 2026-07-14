package jobrequests

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestBuildJobRequestFilter(t *testing.T) {
	pending := buildJobRequestFilter("pending", "artisan1")
	if pending["status"] != "pending" {
		t.Fatalf("pending filter should set status=pending, got %v", pending["status"])
	}
	if _, hasArtisan := pending["artisan_id"]; hasArtisan {
		t.Fatalf("pending pool should NOT scope by artisan_id")
	}
	accepted := buildJobRequestFilter("accepted", "artisan1")
	if accepted["status"] != "accepted" || accepted["artisan_id"] != "artisan1" {
		t.Fatalf("accepted filter should scope status+artisan, got %v", accepted)
	}
	completed := buildJobRequestFilter("completed", "artisan1")
	if completed["status"] != "completed" || completed["artisan_id"] != "artisan1" {
		t.Fatalf("completed filter wrong: %v", completed)
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
