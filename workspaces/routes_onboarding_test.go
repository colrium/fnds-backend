package workspaces

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/onboarding"
	"backend/ratelimit"
	"backend/stytch"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// mustMintTicket mints an onboarding ticket for stateID/stage, failing the
// test on error. SESSION_KEY must already be set via t.Setenv.
func mustMintTicket(t *testing.T, stateID string, stage onboarding.Stage) string {
	t.Helper()
	ticket, err := onboarding.MintTicket(stateID, stage)
	if err != nil {
		t.Fatalf("mint ticket: %v", err)
	}
	return ticket
}

func newOnboardingRouteTestApp(t *testing.T, onboardingService onboardingService, wsService *WorkspaceService) *echo.Echo {
	t.Helper()
	clock := routeTestClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	if wsService == nil {
		wsService = &WorkspaceService{}
	}

	app := echo.New()
	RegisterRoutes(RouteConfig{
		App: app,
		Adapter: stytch.NewWithClient(stytch.Config{
			ProjectID:     "project-test",
			Secret:        "secret-test",
			PublicToken:   "public-token-test",
			Environment:   "test",
			WebhookSecret: "whsec_test",
			Clock:         clock,
		}, stytch.NewFake(clock)),
		AuditLogger:       audit.NewLogger(),
		RateLimiter:       ratelimit.NewLimiter(nil, nil),
		WorkspaceService:  wsService,
		MembershipService: &MembershipService{},
		OnboardingService: onboardingService,
		WebhookConsumer:   &WebhookConsumer{},
		HealthHandler:     &HealthHandler{},
		MigrationHandler:  &MigrationHandler{},
		Registry:          NewRouteRegistry(),
		Clock:             clock,
	})
	return app
}

func TestLinkDecisionRoute(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	// A decision that still needs a challenge is the remaining case that yields a
	// ticket; a satisfied decision now provisions a default workspace instead.
	t.Run("valid ticket forwards the decision and mints a fresh ticket", func(t *testing.T) {
		svc := &routeTestOnboardingService{
			decideLinkResult: onboarding.LinkResult{Stage: string(onboarding.StageAwaitingLinkDecision)},
		}
		app := newOnboardingRouteTestApp(t, svc, nil)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingLinkDecision)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/link/decision",
			bytes.NewBufferString(`{"candidate_id":"candidate-1"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if svc.decideLinkInput.CandidateID != "candidate-1" {
			t.Errorf("CandidateID = %q, want %q", svc.decideLinkInput.CandidateID, "candidate-1")
		}

		var response struct {
			Data struct {
				Stage            string `json:"stage"`
				OnboardingTicket string `json:"onboarding_ticket"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.Data.Stage != string(onboarding.StageAwaitingLinkDecision) {
			t.Errorf("stage = %q, want %q", response.Data.Stage, onboarding.StageAwaitingLinkDecision)
		}
		claims, err := onboarding.ValidateTicket(response.Data.OnboardingTicket)
		if err != nil {
			t.Fatalf("validate re-minted ticket: %v", err)
		}
		if claims.Stage != onboarding.StageAwaitingLinkDecision {
			t.Errorf("re-minted ticket stage = %q, want %q", claims.Stage, onboarding.StageAwaitingLinkDecision)
		}
	})

	t.Run("missing ticket is rejected", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/link/decision",
			bytes.NewBufferString(`{"candidate_id":"candidate-1"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
		}
	})

	t.Run("service error propagates its status and code", func(t *testing.T) {
		svc := &routeTestOnboardingService{
			decideLinkErr: &onboarding.ServiceError{Status: 409, Code: "email_already_linked", Message: "already linked"},
		}
		app := newOnboardingRouteTestApp(t, svc, nil)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingLinkDecision)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/link/decision",
			bytes.NewBufferString(`{"candidate_id":"candidate-1"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusConflict, recorder.Body.String())
		}
	})
}

func TestLinkChallengeRoute(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	svc := &routeTestOnboardingService{
		issueChallengeResult: onboarding.IssueChallengeResult{
			Stage:       string(onboarding.StageAwaitingLinkDecision),
			Challenge:   string(onboarding.ChallengePhoneOTP),
			MaskedPhone: "*******678",
		},
	}
	app := newOnboardingRouteTestApp(t, svc, nil)
	ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingLinkDecision)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/employer/onboarding/link/challenge", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
	recorder := httptest.NewRecorder()

	app.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response struct {
		Data onboarding.IssueChallengeResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.MaskedPhone != "*******678" {
		t.Errorf("MaskedPhone = %q, want %q", response.Data.MaskedPhone, "*******678")
	}
}

func TestLinkChallengeVerifyRoute(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	// A correct code satisfies the link and would previously have re-minted a
	// ticket for the naming step. That step is gone: a default workspace is
	// provisioned on the spot, so the response must carry no ticket. The Stytch
	// org-create is forced to fail so provisioning returns before its first Mongo
	// write, keeping this test DB-free while still proving which path ran.
	t.Run("correct code provisions a default workspace instead of minting a ticket", func(t *testing.T) {
		svc := &routeTestOnboardingService{
			verifyChallengeResult: onboarding.LinkResult{Stage: string(onboarding.StageAwaitingWorkspaceName)},
			stateByIDResult: &onboarding.OnboardingState{
				ID:    "state-1",
				Stage: onboarding.StageAwaitingWorkspaceName,
				Email: "employer@example.com",
			},
		}
		wsService, fake := newProvisioningTestService(t)
		fake.FailAt["CreateOrganizationFromDiscovery"] = []stytch.FailSpec{
			{Attempt: 1, Kind: stytch.KindInvalidRequest, Status: 400},
		}
		app := newOnboardingRouteTestApp(t, svc, wsService)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingLinkDecision)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/link/challenge/verify",
			bytes.NewBufferString(`{"code":" 123456 "}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if svc.verifyChallengeInput != "123456" {
			t.Errorf("verify input = %q, want %q (trimmed)", svc.verifyChallengeInput, "123456")
		}
		if svc.stateByIDInput != "state-1" {
			t.Errorf("StateByID called with %q, want %q — provisioning never began",
				svc.stateByIDInput, "state-1")
		}
		if bytes.Contains(recorder.Body.Bytes(), []byte("onboarding_ticket")) {
			t.Errorf("response still carries an onboarding ticket; body = %s", recorder.Body.String())
		}
	})

	t.Run("incorrect code returns 401", func(t *testing.T) {
		svc := &routeTestOnboardingService{
			verifyChallengeErr: &onboarding.ServiceError{Status: 401, Code: "challenge_incorrect", Message: "incorrect verification code"},
		}
		app := newOnboardingRouteTestApp(t, svc, nil)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingLinkDecision)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/link/challenge/verify",
			bytes.NewBufferString(`{"code":"000000"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
		}
	})
}

func TestSignOutRoute(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	t.Run("bare onboarding ticket is a no-op success", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingWorkspaceName)

		request := httptest.NewRequest(http.MethodPost, "/api/v1/employer/onboarding/sign-out", nil)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
	})

	t.Run("no Authorization header is still a no-op success", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)

		request := httptest.NewRequest(http.MethodPost, "/api/v1/employer/onboarding/sign-out", nil)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
	})

	t.Run("a real Stytch session credential is revoked", func(t *testing.T) {
		clock := routeTestClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
		fake := stytch.NewFake(clock)
		adapter := stytch.NewWithClient(stytch.Config{
			ProjectID:     "project-test",
			Secret:        "secret-test",
			PublicToken:   "public-token-test",
			Environment:   "test",
			WebhookSecret: "whsec_test",
			Clock:         clock,
		}, fake)

		app := echo.New()
		RegisterRoutes(RouteConfig{
			App:               app,
			Adapter:           adapter,
			AuditLogger:       audit.NewLogger(),
			RateLimiter:       ratelimit.NewLimiter(nil, nil),
			WorkspaceService:  &WorkspaceService{},
			MembershipService: &MembershipService{},
			OnboardingService: &routeTestOnboardingService{},
			WebhookConsumer:   &WebhookConsumer{},
			HealthHandler:     &HealthHandler{},
			MigrationHandler:  &MigrationHandler{},
			Registry:          NewRouteRegistry(),
			Clock:             clock,
		})

		// A locally-verified RS256 session JWT carries no session ID (see
		// stytch/jwks.go's VerifySessionJWT — it extracts only sub/org_id),
		// so revocation is only possible for the opaque-token credential
		// shape, whose AuthenticateSession round trip does return one.
		createResult, err := adapter.Client().CreateOrganizationFromDiscovery(t.Context(), stytch.CreateOrgInput{
			IntermediateSessionToken: "intermediate-token",
			Name:                     "Acme Ltd",
			Slug:                     "acme-ltd",
		})
		if err != nil {
			t.Fatalf("seed organization/session: %v", err)
		}

		request := httptest.NewRequest(http.MethodPost, "/api/v1/employer/onboarding/sign-out", nil)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+createResult.SessionToken)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if fake.CallCount("RevokeSession") != 1 {
			t.Errorf("RevokeSession calls = %d, want 1", fake.CallCount("RevokeSession"))
		}
	})
}

func TestWorkspaceProvisionRoute(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	t.Run("missing ticket is rejected", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/workspace",
			bytes.NewBufferString(`{"workspace_name":"Acme Ltd"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
		}
	})

	t.Run("invalid workspace name is rejected before any state lookup", func(t *testing.T) {
		svc := &routeTestOnboardingService{}
		app := newOnboardingRouteTestApp(t, svc, nil)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingWorkspaceName)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/workspace",
			bytes.NewBufferString(`{"workspace_name":"a"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
		if svc.stateByIDInput != "" {
			t.Errorf("StateByID should not have been called, got input %q", svc.stateByIDInput)
		}
	})

	t.Run("wrong stage is rejected", func(t *testing.T) {
		svc := &routeTestOnboardingService{
			stateByIDResult: &onboarding.OnboardingState{ID: "state-1", Stage: onboarding.StageAwaitingLinkDecision},
		}
		app := newOnboardingRouteTestApp(t, svc, nil)
		ticket := mustMintTicket(t, "state-1", onboarding.StageAwaitingLinkDecision)

		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/employer/onboarding/workspace",
			bytes.NewBufferString(`{"workspace_name":"Acme Ltd"}`),
		)
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
		recorder := httptest.NewRecorder()

		app.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusConflict, recorder.Body.String())
		}
	})

	t.Run("happy path creates the workspace and returns a real session", func(t *testing.T) {
		mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

		mt.Run("provision", func(mt *mtest.T) {
			previous := configs.MI
			configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
			mt.Cleanup(func() { configs.MI = previous })

			nsOf := func(coll string) string { return mt.DB.Name() + "." + coll }

			mt.AddMockResponses(
				mtest.CreateSuccessResponse(), // insert workspace
				mtest.CreateSuccessResponse(), // insert membership
				mtest.CreateSuccessResponse(), // insert account_link (Req 5.8)
				mtest.CreateSuccessResponse(), // audit log entry
				mtest.CreateCursorResponse(0, nsOf("memberships"), mtest.FirstBatch, bson.D{
					{Key: "_id", Value: "membership-1"},
					{Key: "workspace_id", Value: "mocked-ws-id"},
					{Key: "user_id", Value: "user-1"},
					{Key: "role", Value: "owner"},
					{Key: "status", Value: "active"},
					{Key: "authority_version", Value: int64(1)},
					{Key: "last_active_at", Value: time.Now()},
					{Key: "joined_at", Value: time.Now()},
					{Key: "created_at", Value: time.Now()},
					{Key: "updated_at", Value: time.Now()},
				}),
			)

			clock := routeTestClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
			fake := stytch.NewFake(clock)
			adapter := stytch.NewWithClient(stytch.Config{
				ProjectID:     "project-test",
				Secret:        "secret-test",
				PublicToken:   "public-token-test",
				Environment:   "test",
				WebhookSecret: "whsec_test",
				Clock:         clock,
			}, fake)
			wsService := NewWorkspaceService(adapter, audit.NewLogger(), clock, ratelimit.NewLimiter(nil, nil))

			svc := &routeTestOnboardingService{
				stateByIDResult: &onboarding.OnboardingState{
					ID:                       "state-1",
					Stage:                    onboarding.StageAwaitingWorkspaceName,
					Email:                    "employer@example.com",
					UserID:                   "user-1",
					IntermediateSessionToken: "intermediate-token",
				},
			}

			app := newOnboardingRouteTestApp(t, svc, wsService)
			ticket, err := onboarding.MintTicket("state-1", onboarding.StageAwaitingWorkspaceName)
			if err != nil {
				mt.Fatalf("mint ticket: %v", err)
			}

			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/employer/onboarding/workspace",
				bytes.NewBufferString(`{"workspace_name":"Acme Ltd"}`),
			)
			request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			request.Header.Set(echo.HeaderAuthorization, "Bearer "+ticket)
			recorder := httptest.NewRecorder()

			app.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				mt.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}

			var response struct {
				Data authenticateResponse `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				mt.Fatalf("decode response: %v", err)
			}
			if response.Data.Stage != string(onboarding.StageComplete) {
				mt.Errorf("stage = %q, want %q", response.Data.Stage, onboarding.StageComplete)
			}
			if response.Data.SessionJWT == "" {
				mt.Error("expected a non-empty session_jwt")
			}
			if response.Data.PostAuth == nil || response.Data.PostAuth.ActiveWorkspaceID != "mocked-ws-id" {
				mt.Errorf("PostAuth = %+v, want ActiveWorkspaceID %q", response.Data.PostAuth, "mocked-ws-id")
			}
			if svc.completeStateID != "state-1" {
				mt.Errorf("CompleteOnboarding stateID = %q, want %q", svc.completeStateID, "state-1")
			}
		})
	})
}
