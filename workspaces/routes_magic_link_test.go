package workspaces

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/audit"
	"backend/onboarding"
	"backend/ratelimit"
	"backend/stytch"

	"github.com/labstack/echo/v4"
)

type routeTestClock struct {
	now time.Time
}

type routeTestOnboardingService struct {
	initiateResult onboarding.InitiateResult
	initiateErr    error
	authResult     onboarding.AuthResult
	authErr        error
	authInput      onboarding.AuthenticateInput

	stateByIDResult *onboarding.OnboardingState
	stateByIDErr    error
	stateByIDInput  string

	decideLinkResult onboarding.LinkResult
	decideLinkErr    error
	decideLinkInput  onboarding.LinkDecision

	issueChallengeResult onboarding.IssueChallengeResult
	issueChallengeErr    error

	verifyChallengeResult onboarding.LinkResult
	verifyChallengeErr    error
	verifyChallengeInput  string

	completeErr         error
	completeStateID     string
	completeWorkspaceID string

	submitNameErr   error
	submitNameFirst string
	submitNameLast  string
}

func (s *routeTestOnboardingService) InitiateMagicLink(
	context.Context,
	onboarding.InitiateInput,
) (onboarding.InitiateResult, error) {
	return s.initiateResult, s.initiateErr
}

func (s *routeTestOnboardingService) Authenticate(
	_ context.Context,
	input onboarding.AuthenticateInput,
) (onboarding.AuthResult, error) {
	s.authInput = input
	return s.authResult, s.authErr
}

func (s *routeTestOnboardingService) StateByID(_ context.Context, stateID string) (*onboarding.OnboardingState, error) {
	s.stateByIDInput = stateID
	return s.stateByIDResult, s.stateByIDErr
}

func (s *routeTestOnboardingService) DecideLink(
	_ context.Context,
	_ string,
	decision onboarding.LinkDecision,
) (onboarding.LinkResult, error) {
	s.decideLinkInput = decision
	return s.decideLinkResult, s.decideLinkErr
}

func (s *routeTestOnboardingService) IssueLinkChallenge(context.Context, string) (onboarding.IssueChallengeResult, error) {
	return s.issueChallengeResult, s.issueChallengeErr
}

func (s *routeTestOnboardingService) VerifyLinkChallenge(_ context.Context, _ string, code string) (onboarding.LinkResult, error) {
	s.verifyChallengeInput = code
	return s.verifyChallengeResult, s.verifyChallengeErr
}

func (s *routeTestOnboardingService) SubmitName(_ context.Context, stateID, first, last string) error {
	s.stateByIDInput = stateID
	s.submitNameFirst = first
	s.submitNameLast = last
	return s.submitNameErr
}

func (s *routeTestOnboardingService) CompleteOnboarding(_ context.Context, stateID, workspaceID string) error {
	s.completeStateID = stateID
	s.completeWorkspaceID = workspaceID
	return s.completeErr
}

func (c routeTestClock) Now() time.Time {
	return c.now
}

func TestMagicLinkRouteSendsEmailThroughStytch(t *testing.T) {
	t.Setenv("APP_PUBLIC_URL", "https://staging.fundis.co.ke")
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	clock := routeTestClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	stytchFake := stytch.NewFake(clock)
	adapter := stytch.NewWithClient(stytch.Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "public-token-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Clock:         clock,
	}, stytchFake)
	limiter := ratelimit.NewLimiter(nil, nil)
	onboardingService := onboarding.NewService(adapter, limiter)

	app := echo.New()
	RegisterRoutes(RouteConfig{
		App:               app,
		Adapter:           adapter,
		AuditLogger:       audit.NewLogger(),
		RateLimiter:       limiter,
		WorkspaceService:  &WorkspaceService{},
		MembershipService: &MembershipService{},
		OnboardingService: onboardingService,
		WebhookConsumer:   &WebhookConsumer{},
		HealthHandler:     &HealthHandler{},
		MigrationHandler:  &MigrationHandler{},
		Registry:          NewRouteRegistry(),
		Clock:             clock,
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/employer/signup/magic-link",
		bytes.NewBufferString(`{"email":" Employer@Example.COM ","turnstile_token":"test-token"}`),
	)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()

	app.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if stytchFake.CallCount("DiscoveryEmailSend") != 1 {
		t.Fatalf("DiscoveryEmailSend calls = %d, want 1", stytchFake.CallCount("DiscoveryEmailSend"))
	}

	input, ok := stytchFake.Calls[0].Args.(stytch.DiscoveryEmailSendInput)
	if !ok {
		t.Fatalf("DiscoveryEmailSend args = %T, want stytch.DiscoveryEmailSendInput", stytchFake.Calls[0].Args)
	}
	if input.Email != "employer@example.com" {
		t.Errorf("email = %q, want %q", input.Email, "employer@example.com")
	}
	if input.RedirectURL != "https://staging.fundis.co.ke/employer/authenticate" {
		t.Errorf(
			"redirect URL = %q, want %q",
			input.RedirectURL,
			"https://staging.fundis.co.ke/employer/authenticate",
		)
	}
}

func TestAuthenticateRouteReturnsOnboardingTicketForNewEmployer(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-with-at-least-32-bytes")

	clock := routeTestClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	limiter := ratelimit.NewLimiter(nil, nil)
	onboardingService := &routeTestOnboardingService{
		authResult: onboarding.AuthResult{
			Stage:        string(onboarding.StageAwaitingWorkspaceName),
			OnboardingID: "onboarding-new-employer",
		},
	}

	app := echo.New()
	RegisterRoutes(RouteConfig{
		App:               app,
		AuditLogger:       audit.NewLogger(),
		RateLimiter:       limiter,
		WorkspaceService:  &WorkspaceService{},
		MembershipService: &MembershipService{},
		OnboardingService: onboardingService,
		WebhookConsumer:   &WebhookConsumer{},
		HealthHandler:     &HealthHandler{},
		MigrationHandler:  &MigrationHandler{},
		Registry:          NewRouteRegistry(),
		Clock:             clock,
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/employer/signup/authenticate",
		bytes.NewBufferString(`{"token":" discovery-token ","type":"magic_link"}`),
	)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()

	app.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if onboardingService.authInput.DiscoveryToken != "discovery-token" {
		t.Errorf("discovery token = %q, want %q", onboardingService.authInput.DiscoveryToken, "discovery-token")
	}
	if onboardingService.authInput.TokenType != "magic_link" {
		t.Errorf("token type = %q, want %q", onboardingService.authInput.TokenType, "magic_link")
	}

	var response struct {
		Data authenticateResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Stage != string(onboarding.StageAwaitingWorkspaceName) {
		t.Errorf("stage = %q, want %q", response.Data.Stage, onboarding.StageAwaitingWorkspaceName)
	}
	if response.Data.OnboardingTicket == "" {
		t.Fatal("onboarding ticket is empty")
	}

	claims, err := onboarding.ValidateTicket(response.Data.OnboardingTicket)
	if err != nil {
		t.Fatalf("validate onboarding ticket: %v", err)
	}
	if claims.StateID != "onboarding-new-employer" {
		t.Errorf("ticket state ID = %q, want %q", claims.StateID, "onboarding-new-employer")
	}
	if claims.Stage != onboarding.StageAwaitingWorkspaceName {
		t.Errorf("ticket stage = %q, want %q", claims.Stage, onboarding.StageAwaitingWorkspaceName)
	}
}
