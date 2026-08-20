package workspaces

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/onboarding"
	"backend/stytch"

	"github.com/labstack/echo/v4"
)

// All three workspace-name endpoints bound `json:"name"` while every frontend
// caller — and design.md's onboarding sequence — sends `workspace_name`. The
// field therefore never bound, so a perfectly valid name arrived as "" and was
// rejected with "workspace_name must be at least 2 characters", an error naming
// a field the handler did not read. A validation test over ValidateName cannot
// catch this: the bug is entirely in the JSON tag, and ValidateName was correct.
//
// The assertion is deliberately "a 5-character name is not rejected for being
// too short" rather than a 200, so the test isolates binding from everything
// downstream (Mongo, Stytch) that these handlers also touch.

const tooShortMessage = "must be at least 2 characters"

func TestOnboardingWorkspaceRouteBindsWorkspaceNameField(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	svc := &routeTestOnboardingService{
		stateByIDResult: &onboarding.OnboardingState{
			ID:    "state-1",
			Stage: onboarding.StageAwaitingWorkspaceName,
			Email: "employer@example.com",
		},
	}
	// A real WorkspaceService whose Stytch org-create fails: once the name binds,
	// the handler proceeds into provisioning, and every Stytch call there
	// precedes the first Mongo write — so failing it returns an ordinary error
	// response instead of dereferencing a nil DB handle.
	wsService, fake := newProvisioningTestService(t)
	fake.FailAt["CreateOrganizationFromDiscovery"] = []stytch.FailSpec{
		{Attempt: 1, Kind: stytch.KindInvalidRequest, Status: 400},
	}
	app := newOnboardingRouteTestApp(t, svc, wsService)

	ticket, err := onboarding.MintTicket("state-1", onboarding.StageAwaitingWorkspaceName)
	if err != nil {
		t.Fatalf("mint ticket: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/employer/onboarding/workspace",
		bytes.NewBufferString(`{"workspace_name":"admin"}`),
	)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)

	if bytes.Contains(recorder.Body.Bytes(), []byte(tooShortMessage)) {
		t.Errorf("a 5-character workspace_name was rejected as too short, so the field did not bind; body = %s",
			recorder.Body.String())
	}
}

// The two session-authenticated handlers share createWorkspaceRequest's shape,
// so decoding it directly pins the tag without standing up a session.
func TestCreateWorkspaceRequestDecodesWorkspaceName(t *testing.T) {
	var request createWorkspaceRequest
	if err := json.Unmarshal([]byte(`{"workspace_name":"admin"}`), &request); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if request.Name != "admin" {
		t.Errorf("Name = %q, want %q — the json tag does not match what clients send", request.Name, "admin")
	}
	if err := ValidateName(request.Name); err != nil {
		t.Errorf("ValidateName rejected a bound 5-character name: %v", err)
	}
}

// "name" must NOT bind: leaving both tags accepted would hide a client still
// sending the wrong field, which is how this shipped in the first place.
func TestCreateWorkspaceRequestIgnoresLegacyNameField(t *testing.T) {
	var request createWorkspaceRequest
	if err := json.Unmarshal([]byte(`{"name":"admin"}`), &request); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if request.Name != "" {
		t.Errorf("Name = %q from a legacy \"name\" payload, want empty", request.Name)
	}
}
