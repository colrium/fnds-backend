package workspaces

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"backend/onboarding"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

// Employers no longer name a workspace during sign-up. One is provisioned for
// them under a derived name, and renaming happens later through workspace
// settings (PATCH /workspaces/:workspaceID). The awaiting_workspace_name stage
// still exists in the state machine — provisioning can fail and land back on
// it, and POST /employer/onboarding/workspace remains the explicit retry — but
// no client is ever asked to satisfy it.

// deriveDefaultWorkspaceName picks the name a new employer's workspace gets,
// using only what the onboarding state already holds.
//
// It performs no database read on purpose. The migration's DeriveWorkspaceName
// prefers the employer's newest company name, but that is meaningless here: an
// employer reaching this point has just signed up and owns no company yet. And
// naming must not be able to fail — a workspace the employer can rename is a
// far better outcome than a blocked sign-up, so there is nothing to degrade
// from and no error to handle.
//
// Preference order: profile name, then email local part, then a placeholder.
func deriveDefaultWorkspaceName(state *onboarding.OnboardingState) string {
	if name := strings.TrimSpace(state.FirstName + " " + state.LastName); ValidateName(name) == nil {
		return name
	}
	if local := emailLocalPart(state.Email); local != "" {
		return local
	}
	// Guaranteed to satisfy ValidateName, so provisioning cannot be rejected
	// for a name the employer never chose.
	return "Workspace"
}

// emailLocalPart returns the part before "@", trimmed, when it is long enough to
// satisfy ValidateName. A local part of one character ("a@x.com") would be
// rejected as too short, so it is refused here rather than producing a name the
// validator then throws out.
func emailLocalPart(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return ""
	}
	local := strings.TrimSpace(email[:at])
	if ValidateName(local) != nil {
		return ""
	}
	return local
}

// completeWithDefaultWorkspace provisions a workspace under a derived name when
// onboarding has reached awaiting_workspace_name, so the client is handed a
// finished session instead of an onboarding ticket and a naming prompt.
//
// Returns handled=false when the stage is anything else, leaving the caller's
// normal ticket-minting path untouched.
func completeWithDefaultWorkspace(c echo.Context, cfg RouteConfig, stateID, stage string) (bool, error) {
	if onboarding.Stage(stage) != onboarding.StageAwaitingWorkspaceName {
		return false, nil
	}

	ctx := c.Request().Context()
	state, err := cfg.OnboardingService.StateByID(ctx, stateID)
	if err != nil {
		return true, respondOnboardingServiceError(c, err,
			"auth_provider_unavailable", "could not load the onboarding session")
	}

	name := deriveDefaultWorkspaceName(state)
	return true, provisionOnboardingWorkspace(c, cfg, state, name)
}

// provisionOnboardingWorkspace creates the workspace, completes onboarding, and
// returns the finished session. Shared by the automatic path above and the
// explicit POST /employer/onboarding/workspace retry so the two cannot drift.
func provisionOnboardingWorkspace(
	c echo.Context,
	cfg RouteConfig,
	state *onboarding.OnboardingState,
	rawName string,
) error {
	ctx := c.Request().Context()

	result, err := cfg.WorkspaceService.CreateForOnboarding(ctx, CreateForOnboardingInput{
		OnboardingStateID:   state.ID,
		StytchOrgID:         state.StytchOrganizationID,
		IntermediateSession: state.IntermediateSessionToken,
		UserID:              state.UserID,
		Email:               state.Email,
		StytchMemberID:      state.StytchMemberID,
		RawName:             rawName,
	})
	if err != nil {
		var wsErr *WorkspaceError
		if errors.As(err, &wsErr) {
			return responses.SendErrorWithCode(c, wsErr.Status, wsErr.Code, wsErr.Message, "", "", nil)
		}
		slog.ErrorContext(ctx, "employer onboarding: create workspace failed", "error", err)
		return responses.SendErrorWithCode(
			c,
			http.StatusServiceUnavailable,
			"workspace_provisioning_failed",
			"could not create the workspace; please try again",
			"",
			"try_again",
			nil,
		)
	}

	if err := cfg.OnboardingService.CompleteOnboarding(ctx, state.ID, result.Workspace.ID); err != nil {
		return respondOnboardingServiceError(c, err, "auth_provider_unavailable", "could not complete onboarding")
	}

	postAuth, postAuthErr := cfg.WorkspaceService.ResolvePostAuth(ctx, state.UserID, state.Email)
	if postAuthErr != nil {
		slog.ErrorContext(ctx, "employer onboarding: resolve post-auth failed", "error", postAuthErr)
		return responses.SendErrorWithCode(
			c,
			http.StatusServiceUnavailable,
			"auth_provider_unavailable",
			"could not complete employer onboarding",
			"",
			"try_again",
			nil,
		)
	}

	return responses.SendOK(c, authenticateResponse{
		Stage:      string(onboarding.StageComplete),
		SessionJWT: result.SessionJWT,
		PostAuth:   &postAuth,
	}, "Workspace created")
}
