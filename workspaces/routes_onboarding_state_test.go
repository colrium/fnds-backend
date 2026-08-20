package workspaces

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/onboarding"

	"github.com/labstack/echo/v4"
)

// GET /employer/onboarding/state and POST /employer/onboarding/name were
// registered as handlers returning {"success": true} unconditionally. Both are
// on the design's onboarding-ticket allow-list (design.md's onboardingGroup
// table), and the service methods behind them — StateByID and SubmitName —
// already existed, so only the HTTP layer was missing.

func TestOnboardingStateRoute(t *testing.T) {
	t.Run("returns the current stage for a valid ticket", func(t *testing.T) {
		t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")
		svc := &routeTestOnboardingService{
			stateByIDResult: &onboarding.OnboardingState{
				ID:        "state-1",
				Stage:     onboarding.StageAwaitingWorkspaceName,
				Email:     "employer@example.com",
				FirstName: "Ada",
				LastName:  "Lovelace",
			},
		}
		app := newOnboardingRouteTestApp(t, svc, nil)

		ticket, err := onboarding.MintTicket("state-1", onboarding.StageAwaitingWorkspaceName)
		if err != nil {
			t.Fatalf("mint ticket: %v", err)
		}

		request := httptest.NewRequest(http.MethodGet, "/api/v1/employer/onboarding/state", nil)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
		}
		if svc.stateByIDInput != "state-1" {
			t.Errorf("StateByID called with %q, want %q", svc.stateByIDInput, "state-1")
		}

		var body struct {
			Data onboardingStateResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v; raw = %s", err, recorder.Body.String())
		}
		if body.Data.Stage != string(onboarding.StageAwaitingWorkspaceName) {
			t.Errorf("stage = %q, want %q", body.Data.Stage, onboarding.StageAwaitingWorkspaceName)
		}
		if body.Data.FirstName != "Ada" || body.Data.LastName != "Lovelace" {
			t.Errorf("name = %q %q, want Ada Lovelace", body.Data.FirstName, body.Data.LastName)
		}

		// The intermediate session token and challenge material must never
		// reach the client, which is why the handler maps to a narrow
		// response type instead of returning the OnboardingState.
		if bytes.Contains(recorder.Body.Bytes(), []byte("intermediate")) {
			t.Error("response must not expose the Stytch intermediate session token")
		}
	})

	t.Run("rejects a missing ticket without consulting the service", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)

		request := httptest.NewRequest(http.MethodGet, "/api/v1/employer/onboarding/state", nil)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body = %s", recorder.Code, recorder.Body.String())
		}
		if svc.stateByIDInput != "" {
			t.Errorf("StateByID must not be called without a valid ticket, got %q", svc.stateByIDInput)
		}
	})
}

func TestOnboardingNameRoute(t *testing.T) {
	t.Run("persists the submitted name", func(t *testing.T) {
		t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)

		ticket, err := onboarding.MintTicket("state-1", onboarding.StageAwaitingWorkspaceName)
		if err != nil {
			t.Fatalf("mint ticket: %v", err)
		}

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/name",
			bytes.NewBufferString(`{"first_name":"Ada","last_name":"Lovelace"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
		}
		// The stub returned 200 while discarding the body — assert the values
		// actually reached the service, which is the behavior that was missing.
		if svc.submitNameFirst != "Ada" || svc.submitNameLast != "Lovelace" {
			t.Errorf("SubmitName got %q %q, want Ada Lovelace", svc.submitNameFirst, svc.submitNameLast)
		}
		if svc.stateByIDInput != "state-1" {
			t.Errorf("SubmitName got state %q, want %q", svc.stateByIDInput, "state-1")
		}
	})

	t.Run("rejects a missing ticket without consulting the service", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/name",
			bytes.NewBufferString(`{"first_name":"Ada"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body = %s", recorder.Code, recorder.Body.String())
		}
		if svc.submitNameFirst != "" {
			t.Errorf("SubmitName must not be called without a valid ticket, got %q", svc.submitNameFirst)
		}
	})

	t.Run("surfaces a service error instead of a canned success", func(t *testing.T) {
		t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")
		svc := &routeTestOnboardingService{
			submitNameErr: &onboarding.ServiceError{
				Status:  400,
				Code:    "invalid_name",
				Message: "first name is required",
				Field:   "first_name",
			},
		}
		app := newOnboardingRouteTestApp(t, svc, nil)

		ticket, err := onboarding.MintTicket("state-1", onboarding.StageAwaitingWorkspaceName)
		if err != nil {
			t.Fatalf("mint ticket: %v", err)
		}

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/name",
			bytes.NewBufferString(`{"first_name":"","last_name":""}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
		}
		if !bytes.Contains(recorder.Body.Bytes(), []byte("invalid_name")) {
			t.Errorf("body should carry the service's code, got %s", recorder.Body.String())
		}
	})
}
