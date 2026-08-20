package workspaces

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"backend/audit"
	"backend/configs"
	"backend/contextutil"
	"backend/middleware"
	"backend/onboarding"
	"backend/ratelimit"
	"backend/responses"
	"backend/stytch"
	"backend/users"

	"github.com/labstack/echo/v4"
)

// RouteConfig holds all the dependencies needed for route registration.
type RouteConfig struct {
	App               *echo.Echo
	Adapter           *stytch.Adapter
	AuditLogger       *audit.Logger
	RateLimiter       ratelimit.RateLimiter
	WorkspaceService  *WorkspaceService
	MembershipService *MembershipService
	Invitations       InvitationHandlers // built by invitations.NewHTTPHandlers — see invitation_handlers.go
	OnboardingService onboardingService
	WebhookConsumer   *WebhookConsumer
	HealthHandler     *HealthHandler
	MigrationHandler  *MigrationHandler
	Registry          *RouteRegistry
	Clock             stytch.Clock
}

type onboardingService interface {
	InitiateMagicLink(context.Context, onboarding.InitiateInput) (onboarding.InitiateResult, error)
	Authenticate(context.Context, onboarding.AuthenticateInput) (onboarding.AuthResult, error)
	StateByID(context.Context, string) (*onboarding.OnboardingState, error)
	DecideLink(context.Context, string, onboarding.LinkDecision) (onboarding.LinkResult, error)
	IssueLinkChallenge(context.Context, string) (onboarding.IssueChallengeResult, error)
	VerifyLinkChallenge(context.Context, string, string) (onboarding.LinkResult, error)
	CompleteOnboarding(context.Context, string, string) error
	SubmitName(context.Context, string, string, string) error
}

// onboardingStateResponse is the body of GET /employer/onboarding/state. It
// exposes only what the frontend needs to resume the flow — deliberately not
// the whole OnboardingState, which holds the Stytch intermediate session
// token and the link-challenge material.
type onboardingStateResponse struct {
	Stage     string `json:"stage"`
	Email     string `json:"email"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// onboardingTicketClaims reads the Bearer Onboarding_Ticket from the
// Authorization header and validates it. Every route in onboardingGroup
// (except /sign-out, which also accepts a real workspace session) is
// authenticated this way instead of through Resolver.Authenticate: the
// caller has no Stytch Member session yet, only the short-lived ticket
// minted by /employer/signup/authenticate (Req 1.4).
func onboardingTicketClaims(c echo.Context) (*onboarding.TicketClaims, error) {
	authHeader := c.Request().Header.Get("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || strings.TrimSpace(parts[1]) == "" {
		return nil, errors.New("workspaces: missing or malformed authorization header")
	}
	return onboarding.ValidateTicket(strings.TrimSpace(parts[1]))
}

// respondOnboardingServiceError maps an onboarding.ServiceError to its
// declared HTTP status/code/message, or falls back to a fixed 503 for any
// other error (an upstream Stytch or MongoDB failure), matching the pattern
// already used by the /employer/signup routes above.
func respondOnboardingServiceError(c echo.Context, err error, fallbackCode, fallbackMessage string) error {
	var serviceErr *onboarding.ServiceError
	if errors.As(err, &serviceErr) {
		return responses.SendErrorWithCode(
			c,
			serviceErr.Status,
			serviceErr.Code,
			serviceErr.Message,
			serviceErr.Field,
			serviceErr.SuggestedAction,
			nil,
		)
	}

	slog.ErrorContext(c.Request().Context(), fallbackMessage, "error", err)
	return responses.SendErrorWithCode(
		c,
		http.StatusServiceUnavailable,
		fallbackCode,
		fallbackMessage,
		"",
		"try_again",
		nil,
	)
}

// invalidOnboardingTicket is the fixed response for every failure to
// validate an Onboarding_Ticket, so no failure mode discloses whether the
// ticket was missing, malformed, expired, or signed for a different
// audience.
func invalidOnboardingTicket(c echo.Context) error {
	return responses.SendErrorWithCode(
		c,
		http.StatusUnauthorized,
		"invalid_onboarding_ticket",
		"onboarding session expired or invalid; please sign in again",
		"",
		"sign_in_again",
		nil,
	)
}

type authenticateResponse struct {
	Stage            string                         `json:"stage"`
	OnboardingTicket string                         `json:"onboarding_ticket,omitempty"`
	LinkCandidates   []onboarding.LinkCandidateView `json:"link_candidates,omitempty"`

	// Populated only when Stage is "complete": a real workspace session for
	// a returning, fully-onboarded member. The onboarding_ticket above is
	// for continuing an in-progress signup, not for authenticated requests.
	SessionJWT string          `json:"session_jwt,omitempty"`
	PostAuth   *PostAuthResult `json:"post_auth,omitempty"`
}

// respondServiceError maps a structured service-layer error (*WorkspaceError,
// *DeletionError, or *MembershipError) to its declared HTTP status and code.
// Any other error is logged and reported as a generic 500 so a bug in a
// service method never leaks an internal error string to the client.
func respondServiceError(c echo.Context, err error) error {
	var wsErr *WorkspaceError
	if errors.As(err, &wsErr) {
		return responses.SendErrorWithCode(c, wsErr.Status, wsErr.Code, wsErr.Message, "", "", nil)
	}
	var delErr *DeletionError
	if errors.As(err, &delErr) {
		return responses.SendErrorWithCode(c, delErr.Status, delErr.Code, delErr.Message, "", "", delErr.Details)
	}
	var memErr *MembershipError
	if errors.As(err, &memErr) {
		return responses.SendErrorWithCode(c, memErr.Status, memErr.Code, memErr.Message, "", "", nil)
	}
	slog.ErrorContext(c.Request().Context(), "workspace route: unexpected service error", "error", err)
	return responses.SendErrorWithCode(c, http.StatusInternalServerError, "internal_error", "an unexpected error occurred", "", "", nil)
}

// handleGetWorkspace is the handler for GET /workspaces/:workspaceID.
func handleGetWorkspace(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		ws, err := cfg.WorkspaceService.ByID(c.Request().Context(), c.Param("workspaceID"))
		if err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, ws, "Workspace retrieved")
	}
}

// handleRenameWorkspace is the handler for PATCH /workspaces/:workspaceID.
func handleRenameWorkspace(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		var request struct {
			Name string `json:"workspace_name"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest, "invalid_request", "invalid request body", "", "", nil)
		}

		workspaceID := c.Param("workspaceID")
		ctx := c.Request().Context()
		if err := cfg.WorkspaceService.Rename(ctx, workspaceID, request.Name, sc.UserID); err != nil {
			return respondServiceError(c, err)
		}

		ws, err := cfg.WorkspaceService.ByID(ctx, workspaceID)
		if err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, ws, "Workspace renamed")
	}
}

// handleScheduleDeletion is the handler for POST /workspaces/:workspaceID/deletion.
func handleScheduleDeletion(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		var request struct {
			Confirmation string `json:"confirmation"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest, "invalid_request", "invalid request body", "", "", nil)
		}

		workspaceID := c.Param("workspaceID")
		if err := cfg.WorkspaceService.ScheduleDeletion(c.Request().Context(), workspaceID, request.Confirmation, sc.UserID, sc.Role); err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, nil, "Workspace deletion scheduled")
	}
}

// handleCancelDeletion is the handler for DELETE /workspaces/:workspaceID/deletion.
func handleCancelDeletion(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}
		workspaceID := c.Param("workspaceID")
		if err := cfg.WorkspaceService.CancelDeletion(c.Request().Context(), workspaceID, sc.UserID); err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, nil, "Workspace deletion cancelled")
	}
}

// workspacePermissionsResponse is the payload for GET /workspaces/:workspaceID/permissions.
type workspacePermissionsResponse struct {
	Role        Role         `json:"role"`
	Permissions []Permission `json:"permissions"`
}

// handleGetWorkspacePermissions is the handler for GET
// /workspaces/:workspaceID/permissions. It reports the caller's own granted
// permissions for this workspace, derived from the already-resolved
// SessionContext — GrantedPermissions(sc.Role) is the same computation the
// resolver used to build sc.Permissions, so no bitset-to-slice helper is
// needed on top of it.
func handleGetWorkspacePermissions(c echo.Context) error {
	sc, ok := FromEcho(c)
	if !ok {
		return unauthorized(c)
	}
	return responses.SendOK(c, workspacePermissionsResponse{
		Role:        sc.Role,
		Permissions: GrantedPermissions(sc.Role),
	}, "Permissions retrieved")
}

// handleImportPersonalData is the handler for POST
// /workspaces/:workspaceID/import-personal-data. RescopeUserData (Req 6.1-6.10)
// moves the caller's own unscoped companies, job_vacancies, and
// user_subscriptions documents into this workspace — it is a re-scoping of
// legacy pre-workspace data, not a GDPR-style export/import. It tolerates
// partial failure per collection (see rescope.go), so a partial failure here
// still reports the partial report rather than discarding it.
func handleImportPersonalData(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}
		workspaceID := c.Param("workspaceID")
		report, err := cfg.WorkspaceService.RescopeUserData(c.Request().Context(), sc.UserID, workspaceID)
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "import personal data: partial failure",
				"workspace_id", workspaceID, "error", err)
			return responses.SendErrorWithCode(c, http.StatusInternalServerError,
				"rescope_partial_failure",
				"some personal data could not be imported into this workspace",
				"", "retry",
				map[string]any{"report": report},
			)
		}
		return responses.SendOK(c, report, "Personal data imported into workspace")
	}
}

// handleListMembers is the handler for GET /workspaces/:workspaceID/members.
func handleListMembers(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		workspaceID := c.Param("workspaceID")
		members, err := cfg.MembershipService.List(c.Request().Context(), workspaceID)
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "list members: failed", "workspace_id", workspaceID, "error", err)
			return responses.SendErrorWithCode(c, http.StatusInternalServerError, "internal_error", "could not load members", "", "", nil)
		}
		return responses.SendOK(c, members, "Members retrieved")
	}
}

// handleChangeMemberRole is the handler for PATCH
// /workspaces/:workspaceID/members/:membershipID. ChangeRole runs the guard
// table (ValidateRoleChange) internally before writing, so the handler does
// not need to call it separately.
func handleChangeMemberRole(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		var request struct {
			Role string `json:"role"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest, "invalid_request", "invalid request body", "", "", nil)
		}

		workspaceID := c.Param("workspaceID")
		membershipID := c.Param("membershipID")
		err := cfg.MembershipService.ChangeRole(
			c.Request().Context(), workspaceID, membershipID, sc.MembershipID, sc.Role, Role(request.Role), sc.UserID,
		)
		if err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, nil, "Member role changed")
	}
}

// handleSetMemberStatus builds a handler for the three status-transition
// routes (remove, suspend, reactivate). SetStatus takes the target
// membership's current role as an explicit parameter rather than looking it
// up itself, so the handler fetches it first via MembershipService.ByID.
func handleSetMemberStatus(cfg RouteConfig, newStatus MembershipStatus, successMessage string) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		workspaceID := c.Param("workspaceID")
		membershipID := c.Param("membershipID")
		ctx := c.Request().Context()

		target, err := cfg.MembershipService.ByID(ctx, workspaceID, membershipID)
		if err != nil {
			return respondServiceError(c, err)
		}

		err = cfg.MembershipService.SetStatus(
			ctx, workspaceID, membershipID, sc.MembershipID, sc.Role, target.Role, newStatus, sc.UserID,
		)
		if err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, nil, successMessage)
	}
}

// handleTransferOwnership is the handler for POST
// /workspaces/:workspaceID/ownership. TransferOwnership runs
// ValidateTransferTarget internally before writing, so the handler does not
// need to call it separately.
func handleTransferOwnership(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		var request struct {
			TargetMembershipID string `json:"target_membership_id"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest, "invalid_request", "invalid request body", "", "", nil)
		}

		workspaceID := c.Param("workspaceID")
		err := cfg.MembershipService.TransferOwnership(
			c.Request().Context(), workspaceID, sc.MembershipID, request.TargetMembershipID, sc.UserID,
		)
		if err != nil {
			return respondServiceError(c, err)
		}
		return responses.SendOK(c, nil, "Ownership transferred")
	}
}

// RegisterRoutes registers all workspace-related route groups on the Echo app.
//
// Four groups:
// 1. Unauthenticated (public): signup, invitation resolution, webhooks, health
// 2. Onboarding-ticket scoped: the 7 onboarding paths
// 3. Session-scoped (authenticated, no workspace): /workspaces list/create/switch
// 4. Workspace-scoped (ScopedGroup): all 37 workspace-scoped routes
//
// Requirements: 8.5, 8.12, 22.5, 22.7
func RegisterRoutes(cfg RouteConfig) {
	api := cfg.App.Group("/api/v1")
	resolver := NewResolver(cfg.Adapter, cfg.Clock)

	// ─── 1. Unauthenticated routes ───────────────────────────────────
	// Signup endpoints with rate limiting.
	api.POST("/employer/signup/magic-link", func(c echo.Context) error {
		var request struct {
			Email          string `json:"email"`
			TurnstileToken string `json:"turnstile_token"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"invalid request body",
				"",
				"",
				nil,
			)
		}

		appURL := strings.TrimRight(configs.AppPublicURL(), "/")
		if appURL == "" {
			slog.ErrorContext(c.Request().Context(), "employer magic link: APP_PUBLIC_URL is not configured")
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_configuration_error",
				"employer sign-in is temporarily unavailable",
				"",
				"try_again",
				nil,
			)
		}

		result, err := cfg.OnboardingService.InitiateMagicLink(
			c.Request().Context(),
			onboarding.InitiateInput{
				Email:          request.Email,
				TurnstileToken: request.TurnstileToken,
				IP:             c.RealIP(),
				RedirectURL:    appURL + "/employer/authenticate",
			},
		)
		if err != nil {
			var serviceErr *onboarding.ServiceError
			if errors.As(err, &serviceErr) {
				return responses.SendErrorWithCode(
					c,
					serviceErr.Status,
					serviceErr.Code,
					serviceErr.Message,
					serviceErr.Field,
					serviceErr.SuggestedAction,
					nil,
				)
			}

			slog.ErrorContext(c.Request().Context(), "employer magic link: send failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_provider_unavailable",
				"could not send the sign-in email",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendOK(c, result, "Magic link sent")
	}, cfg.RateLimiter.Guard(ratelimit.LimitSignupPerIP, ratelimit.SubjectIP))

	// OAuth discovery start is not wired yet (task 3.5). The route is
	// registered so the path is reserved and documented, but it must report
	// that plainly — a 200 here would tell a caller a sign-in flow started
	// when nothing happened.
	api.POST("/employer/signup/oauth/:provider", func(c echo.Context) error {
		return responses.SendErrorWithCode(
			c,
			http.StatusNotImplemented,
			"not_implemented",
			"OAuth signup is not available yet",
			"",
			"",
			nil,
		)
	})

	api.POST("/employer/signup/authenticate", func(c echo.Context) error {
		var request struct {
			Token          string `json:"token"`
			DiscoveryToken string `json:"discovery_token"`
			Type           string `json:"type"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"invalid request body",
				"",
				"",
				nil,
			)
		}

		token := strings.TrimSpace(request.Token)
		if token == "" {
			token = strings.TrimSpace(request.DiscoveryToken)
		}
		if token == "" {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"token_required",
				"authentication token is required",
				"token",
				"",
				nil,
			)
		}

		result, err := cfg.OnboardingService.Authenticate(
			c.Request().Context(),
			onboarding.AuthenticateInput{
				DiscoveryToken: token,
				TokenType:      strings.TrimSpace(request.Type),
			},
		)
		if err != nil {
			var serviceErr *onboarding.ServiceError
			if errors.As(err, &serviceErr) {
				return responses.SendErrorWithCode(
					c,
					serviceErr.Status,
					serviceErr.Code,
					serviceErr.Message,
					serviceErr.Field,
					serviceErr.SuggestedAction,
					nil,
				)
			}

			slog.ErrorContext(c.Request().Context(), "employer authentication: token redemption failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_provider_unavailable",
				"could not complete employer authentication",
				"",
				"try_again",
				nil,
			)
		}

		// A returning, fully-onboarded member re-authenticating gets a real
		// workspace session instead of an onboarding ticket: the ticket only
		// carries onboarding stage/state, and there is no more onboarding
		// left to do.
		if onboarding.Stage(result.Stage) == onboarding.StageComplete {
			var sess stytch.Session
			exchangeErr := cfg.Adapter.Do(c.Request().Context(), "ExchangeSession", true, func(attemptCtx context.Context) error {
				var innerErr error
				sess, innerErr = cfg.Adapter.Client().ExchangeSession(attemptCtx, result.IntermediateSessionToken, result.StytchOrganizationID)
				return innerErr
			})
			if exchangeErr != nil {
				slog.ErrorContext(c.Request().Context(), "employer authentication: session exchange failed", "error", exchangeErr)
				return responses.SendErrorWithCode(
					c,
					http.StatusServiceUnavailable,
					"auth_provider_unavailable",
					"could not complete employer authentication",
					"",
					"try_again",
					nil,
				)
			}

			// ResolvePostAuth's second argument is the email, which it needs to
			// find pending invitations addressed to this person — passing the
			// workspace ID here silently matched zero invitations.
			postAuth, postAuthErr := cfg.WorkspaceService.ResolvePostAuth(c.Request().Context(), result.UserID, result.Email)
			if postAuthErr != nil {
				slog.ErrorContext(c.Request().Context(), "employer authentication: resolve post-auth failed", "error", postAuthErr)
				return responses.SendErrorWithCode(
					c,
					http.StatusServiceUnavailable,
					"auth_provider_unavailable",
					"could not complete employer authentication",
					"",
					"try_again",
					nil,
				)
			}

			return responses.SendOK(c, authenticateResponse{
				Stage:      result.Stage,
				SessionJWT: sess.SessionJWT,
				PostAuth:   &postAuth,
			}, "Employer authentication completed")
		}

		ticket, err := onboarding.MintTicket(result.OnboardingID, onboarding.Stage(result.Stage))
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "employer authentication: mint onboarding ticket", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_configuration_error",
				"employer sign-in is temporarily unavailable",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendOK(c, authenticateResponse{
			Stage:            result.Stage,
			OnboardingTicket: ticket,
			LinkCandidates:   result.LinkCandidates,
		}, "Employer authentication completed")
	})

	// Invitation public endpoints (Req 11.1, 11.18, 12.5, 12.6, 25.6). Rate
	// limited per-IP like every other unauthenticated route in this group.
	api.GET("/invitations/:token", cfg.Invitations.Resolve,
		cfg.RateLimiter.Guard(ratelimit.LimitInviteResolveMin, ratelimit.SubjectIP))

	api.POST("/invitations/:token/decline", cfg.Invitations.Decline,
		cfg.RateLimiter.Guard(ratelimit.LimitInviteResolveMin, ratelimit.SubjectIP))

	api.POST("/invitations/:token/accept", cfg.Invitations.Accept,
		cfg.RateLimiter.Guard(ratelimit.LimitInviteResolveMin, ratelimit.SubjectIP))

	// Webhook endpoint — registered before body-consuming middleware. Public
	// and unauthenticated, so it needs IP-based limiting like any other
	// unauthenticated endpoint to bound the cost of garbage POSTs.
	api.POST("/stytch/webhooks", cfg.WebhookConsumer.Handle, cfg.RateLimiter.Guard(ratelimit.LimitWebhookPerIP, ratelimit.SubjectIP))

	// Health endpoint — unauthenticated, no secrets.
	api.GET("/health/identity", cfg.HealthHandler.HandleHealth)

	// ─── 2. Onboarding-ticket scoped routes ──────────────────────────
	onboardingGroup := api.Group("/employer/onboarding")

	// Lets a caller holding a ticket recover which step they are on — a
	// reloaded tab keeps the ticket in sessionStorage but loses the in-memory
	// stage, and the ticket's own stage claim is a snapshot from mint time, so
	// the document is the only current source.
	onboardingGroup.GET("/state", func(c echo.Context) error {
		claims, err := onboardingTicketClaims(c)
		if err != nil {
			return invalidOnboardingTicket(c)
		}

		state, err := cfg.OnboardingService.StateByID(c.Request().Context(), claims.StateID)
		if err != nil {
			return respondOnboardingServiceError(c, err,
				"onboarding_state_unavailable", "could not load your onboarding progress")
		}

		return responses.SendOK(c, onboardingStateResponse{
			Stage:     string(state.Stage),
			Email:     state.Email,
			FirstName: state.FirstName,
			LastName:  state.LastName,
		}, "Onboarding state retrieved")
	})

	// Collects a name when Stytch did not report one (a magic link carries no
	// name, unlike OAuth), so an employer provisioned this way is not left
	// nameless (Req 1.15).
	onboardingGroup.POST("/name", func(c echo.Context) error {
		claims, err := onboardingTicketClaims(c)
		if err != nil {
			return invalidOnboardingTicket(c)
		}

		var request struct {
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest,
				"invalid_request", "invalid request body", "", "", nil)
		}

		if err := cfg.OnboardingService.SubmitName(
			c.Request().Context(), claims.StateID, request.FirstName, request.LastName,
		); err != nil {
			return respondOnboardingServiceError(c, err,
				"onboarding_name_failed", "could not save your name")
		}

		return responses.SendOK(c, nil, "Name saved")
	})
	onboardingGroup.POST("/workspace", func(c echo.Context) error {
		claims, err := onboardingTicketClaims(c)
		if err != nil {
			return invalidOnboardingTicket(c)
		}

		var request struct {
			Name string `json:"workspace_name"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"invalid request body",
				"",
				"",
				nil,
			)
		}

		if err := ValidateName(request.Name); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_workspace_name",
				err.Error(),
				"workspace_name",
				"",
				nil,
			)
		}

		state, err := cfg.OnboardingService.StateByID(c.Request().Context(), claims.StateID)
		if err != nil {
			return respondOnboardingServiceError(c, err, "auth_provider_unavailable", "could not load the onboarding session")
		}

		if state.Stage != onboarding.StageAwaitingWorkspaceName {
			return responses.SendErrorWithCode(
				c,
				http.StatusConflict,
				"invalid_stage",
				"onboarding is not awaiting a workspace name",
				"",
				"",
				nil,
			)
		}

		result, err := cfg.WorkspaceService.CreateForOnboarding(c.Request().Context(), CreateForOnboardingInput{
			OnboardingStateID:   state.ID,
			StytchOrgID:         state.StytchOrganizationID,
			IntermediateSession: state.IntermediateSessionToken,
			UserID:              state.UserID,
			Email:               state.Email,
			StytchMemberID:      state.StytchMemberID,
			RawName:             request.Name,
		})
		if err != nil {
			var wsErr *WorkspaceError
			if errors.As(err, &wsErr) {
				return responses.SendErrorWithCode(c, wsErr.Status, wsErr.Code, wsErr.Message, "", "", nil)
			}
			slog.ErrorContext(c.Request().Context(), "employer onboarding: create workspace failed", "error", err)
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

		if err := cfg.OnboardingService.CompleteOnboarding(c.Request().Context(), state.ID, result.Workspace.ID); err != nil {
			return respondOnboardingServiceError(c, err, "auth_provider_unavailable", "could not complete onboarding")
		}

		postAuth, postAuthErr := cfg.WorkspaceService.ResolvePostAuth(c.Request().Context(), state.UserID, state.Email)
		if postAuthErr != nil {
			slog.ErrorContext(c.Request().Context(), "employer onboarding: resolve post-auth failed", "error", postAuthErr)
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
	})
	onboardingGroup.POST("/link/decision", func(c echo.Context) error {
		claims, err := onboardingTicketClaims(c)
		if err != nil {
			return invalidOnboardingTicket(c)
		}

		var request struct {
			CandidateID    string `json:"candidate_id"`
			CreateSeparate bool   `json:"create_separate"`
			Email          string `json:"email"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"invalid request body",
				"",
				"",
				nil,
			)
		}

		result, err := cfg.OnboardingService.DecideLink(c.Request().Context(), claims.StateID, onboarding.LinkDecision{
			CandidateID:    request.CandidateID,
			CreateSeparate: request.CreateSeparate,
			Email:          request.Email,
		})
		if err != nil {
			return respondOnboardingServiceError(c, err, "auth_provider_unavailable", "could not process the linking decision")
		}

		ticket, err := onboarding.MintTicket(claims.StateID, onboarding.Stage(result.Stage))
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "employer onboarding: mint ticket after link decision", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_configuration_error",
				"employer sign-in is temporarily unavailable",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendOK(c, struct {
			Stage            string `json:"stage"`
			Challenge        string `json:"challenge,omitempty"`
			OnboardingTicket string `json:"onboarding_ticket"`
		}{
			Stage:            result.Stage,
			Challenge:        result.Challenge,
			OnboardingTicket: ticket,
		}, "Link decision recorded")
	})
	onboardingGroup.POST("/link/challenge", func(c echo.Context) error {
		claims, err := onboardingTicketClaims(c)
		if err != nil {
			return invalidOnboardingTicket(c)
		}

		result, err := cfg.OnboardingService.IssueLinkChallenge(c.Request().Context(), claims.StateID)
		if err != nil {
			return respondOnboardingServiceError(c, err, "auth_provider_unavailable", "could not send the verification code")
		}

		return responses.SendOK(c, result, "Verification code sent")
	})
	onboardingGroup.POST("/link/challenge/verify", func(c echo.Context) error {
		claims, err := onboardingTicketClaims(c)
		if err != nil {
			return invalidOnboardingTicket(c)
		}

		var request struct {
			Code string `json:"code"`
		}
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"invalid request body",
				"",
				"",
				nil,
			)
		}

		result, err := cfg.OnboardingService.VerifyLinkChallenge(c.Request().Context(), claims.StateID, strings.TrimSpace(request.Code))
		if err != nil {
			return respondOnboardingServiceError(c, err, "auth_provider_unavailable", "could not verify the code")
		}

		ticket, err := onboarding.MintTicket(claims.StateID, onboarding.Stage(result.Stage))
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "employer onboarding: mint ticket after challenge verify", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_configuration_error",
				"employer sign-in is temporarily unavailable",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendOK(c, struct {
			Stage            string `json:"stage"`
			OnboardingTicket string `json:"onboarding_ticket"`
		}{
			Stage:            result.Stage,
			OnboardingTicket: ticket,
		}, "Verification successful")
	})
	onboardingGroup.POST("/sign-out", func(c echo.Context) error {
		// An Onboarding_Ticket is a bare HS256 JWT scoped to this backend only
		// — Stytch has no record of it, so there is nothing to revoke. Only a
		// genuine Stytch session credential (held after workspace
		// provisioning completes) names a real session worth revoking. This
		// keeps sign-out a single endpoint for both credential shapes instead
		// of forcing the frontend to know which one it currently holds.
		authHeader := c.Request().Header.Get("Authorization")
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			token := strings.TrimSpace(parts[1])
			if token != "" {
				if _, ticketErr := onboarding.ValidateTicket(token); ticketErr != nil {
					id, resolveErr := resolver.authenticateHeader(c)
					if resolveErr == nil && id.sessionID != "" {
						revokeErr := cfg.Adapter.Do(c.Request().Context(), "RevokeSession", false, func(attemptCtx context.Context) error {
							return cfg.Adapter.Client().RevokeSession(attemptCtx, id.sessionID)
						})
						if revokeErr != nil {
							// Best-effort: the frontend discards the credential
							// client-side regardless, so a revoke failure does not
							// block sign-out — it only leaves the session valid at
							// Stytch until its own expiry.
							slog.WarnContext(c.Request().Context(), "employer sign-out: revoke session failed", "error", revokeErr)
						}
					}
				}
			}
		}
		return responses.SendOK(c, map[string]any{"success": true}, "Signed out")
	})

	// ─── 3. Session-scoped routes (authenticated, no workspace required) ───
	// These are excluded from the workspace route registry by name.
	sessionGroup := api.Group("/workspaces")
	sessionGroup.GET("", handleListWorkspaces(cfg), resolver.Authenticate)
	sessionGroup.POST("", handleCreateWorkspace(cfg), resolver.Authenticate)
	sessionGroup.POST("/:workspaceID/switch", handleSwitchWorkspace(cfg), resolver.Authenticate)

	// ─── 4. Workspace-scoped routes (ScopedGroup) ────────────────────
	// Every route here declares a Permission at compile time (Req 8.5).
	handler := func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true, "workspace_id": contextutil.GetWorkspaceID(c)})
	}

	wsSG := NewScopedGroup(api, "/workspaces/:workspaceID", resolver, cfg.Registry)
	wsSG.GET("", PermWorkspaceView, handleGetWorkspace(cfg))
	wsSG.PATCH("", PermWorkspaceRename, handleRenameWorkspace(cfg))
	wsSG.POST("/deletion", PermWorkspaceDelete, handleScheduleDeletion(cfg))
	wsSG.DELETE("/deletion", PermWorkspaceDelete, handleCancelDeletion(cfg))
	wsSG.GET("/permissions", PermWorkspaceView, handleGetWorkspacePermissions)
	wsSG.POST("/import-personal-data", PermWorkspaceImportPersonalData, handleImportPersonalData(cfg))
	wsSG.GET("/members", PermMemberView, handleListMembers(cfg))
	wsSG.PATCH("/members/:membershipID", PermMemberRoleChange, handleChangeMemberRole(cfg))
	wsSG.DELETE("/members/:membershipID", PermMemberRemove, handleSetMemberStatus(cfg, MembershipRemoved, "Member removed"))
	wsSG.POST("/members/:membershipID/suspension", PermMemberRemove, handleSetMemberStatus(cfg, MembershipSuspended, "Member suspended"))
	wsSG.DELETE("/members/:membershipID/suspension", PermMemberRemove, handleSetMemberStatus(cfg, MembershipActive, "Member reactivated"))
	wsSG.POST("/ownership", PermOwnershipTransfer, handleTransferOwnership(cfg))
	wsSG.GET("/invitations", PermMemberView, cfg.Invitations.List)
	wsSG.POST("/invitations", PermMemberInvite, cfg.Invitations.Create)
	wsSG.POST("/invitations/:id/resend", PermMemberInvite, cfg.Invitations.Resend)
	wsSG.DELETE("/invitations/:id", PermMemberInvite, cfg.Invitations.Revoke)
	wsSG.GET("/audit-log", PermAuditView, cfg.AuditLogger.HandleList)

	// Re-scoped legacy employer paths (keep URLs for frontend compatibility).
	vacSG := NewScopedGroup(api, "/job-vacancies", resolver, cfg.Registry)
	vacSG.GET("", PermVacancyView, handler)
	vacSG.POST("", PermVacancyCreate, handler)
	vacSG.POST("/update", PermVacancyUpdate, handler)
	vacSG.POST("/delete", PermVacancyClose, handler)

	vacSingleSG := NewScopedGroup(api, "/job-vacancy", resolver, cfg.Registry)
	vacSingleSG.GET("", PermVacancyView, handler)

	appSG := NewScopedGroup(api, "/job-applications", resolver, cfg.Registry)
	appSG.GET("", PermApplicationView, handler)
	appSG.GET("/:id/review", PermApplicationView, handler)
	appSG.POST("/decision", PermApplicationDecide, handler)

	empSG := NewScopedGroup(api, "/employer", resolver, cfg.Registry)
	empSG.GET("/dashboard", PermVacancyView, handler)
	empSG.GET("/artisans", PermArtisanBrowse, handler)

	coSG := NewScopedGroup(api, "/companies", resolver, cfg.Registry)
	coSG.GET("", PermWorkspaceView, handler)
	coSG.POST("", PermCompanyCreate, handler)
	coSG.POST("/update", PermCompanyUpdate, handler)
	coSG.POST("/delete", PermCompanyUpdate, handler)

	subSG := NewScopedGroup(api, "/subscriptions", resolver, cfg.Registry)
	subSG.GET("/mine", PermBillingView, handler)
	subSG.POST("/purchase", PermSubscriptionManage, handler)
	subSG.POST("/cancel", PermSubscriptionManage, handler)

	invSG := NewScopedGroup(api, "/invoices", resolver, cfg.Registry)
	invSG.GET("", PermInvoiceView, handler)
	invSG.POST("/:id/pay", PermInvoicePay, handler)

	paySG := NewScopedGroup(api, "/payments", resolver, cfg.Registry)
	paySG.GET("", PermPayoutView, handler)

	pmSG := NewScopedGroup(api, "/payment-methods", resolver, cfg.Registry)
	pmSG.POST("", PermPaymentMethodManage, handler)

	// ─── Admin routes (behind middleware.IsAdmin, outside workspace scope) ───
	// These keep the existing pattern and are not registered through ScopedGroup.
	adminGroup := api.Group("/admin")
	adminGroup.Use(middleware.JWTAuth, middleware.IsAdmin)
	adminGroup.POST("/employer-migration", cfg.MigrationHandler.HandleStartMigration)
	adminGroup.GET("/employer-migration/:runID", cfg.MigrationHandler.HandleGetMigrationReport)
	adminGroup.POST("/workspaces/:workspaceID/ownership", handleAdminTransferOwnership(cfg))
}

// adminTransferOwnershipRequest is the body for
// POST /admin/workspaces/:workspaceID/ownership.
type adminTransferOwnershipRequest struct {
	TargetMembershipID string `json:"target_membership_id"`
}

// handleAdminTransferOwnership is the handler for
// POST /admin/workspaces/:workspaceID/ownership (Req 14.13): the platform
// administrator's escape hatch for a workspace whose Owner's user account has
// been suspended, and which therefore cannot use the member-facing
// POST /workspaces/:workspaceID/ownership.
//
// It is deliberately not a ScopedGroup route. The caller holds no membership in
// the target workspace, so there is no SessionContext and no Permission to
// authorize against — middleware.IsAdmin on adminGroup is the whole
// authorization story, and registering this path through a ScopedGroup would
// fail RouteRegistry.Verify at startup.
func handleAdminTransferOwnership(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		adminUserID := contextutil.GetUserID(c)
		if adminUserID == "" {
			return unauthorized(c)
		}

		var request adminTransferOwnershipRequest
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest, "invalid_request", "invalid request body", "", "", nil)
		}
		if strings.TrimSpace(request.TargetMembershipID) == "" {
			return responses.SendErrorWithCode(c, http.StatusBadRequest, "invalid_request", "target_membership_id is required", "", "", nil)
		}

		workspaceID := c.Param("workspaceID")
		err := cfg.MembershipService.AdminTransferOwnership(
			c.Request().Context(),
			workspaceID,
			strings.TrimSpace(request.TargetMembershipID),
			adminUserID,
		)
		if err != nil {
			return respondServiceError(c, err)
		}

		return responses.SendOK(c, map[string]any{
			"workspace_id":         workspaceID,
			"target_membership_id": strings.TrimSpace(request.TargetMembershipID),
		}, "Ownership transferred")
	}
}

// bearerToken extracts the raw bearer credential from the Authorization
// header, exactly the way Resolver.authenticateHeader parses it, without
// the credential-chain verification that follows. Handlers that call a
// Stytch operation needing the raw session credential (ExchangeSession,
// CreateOrganizationFromDiscovery) — rather than the already-resolved
// SessionContext — use this to get it.
func bearerToken(c echo.Context) (string, bool) {
	authHeader := c.Request().Header.Get("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

// handleListWorkspaces lists every workspace the authenticated user belongs
// to, for the workspace selector UI (Req 15.10).
func handleListWorkspaces(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		summaries, err := cfg.WorkspaceService.ListForUser(c.Request().Context(), sc.UserID)
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "list workspaces failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"workspace_list_failed",
				"could not list workspaces",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendOK(c, summaries, "Workspaces retrieved")
	}
}

// createWorkspaceRequest is the body for POST /workspaces (create an
// additional workspace for a user who already holds at least one).
// createWorkspaceRequest and the two inline equivalents (rename, onboarding
// workspace submit) all read workspace_name, not name. That is the field
// design.md's onboarding sequence specifies, the field all three frontend
// callers send, and the field the rejection message names. Binding "name"
// instead bound nothing: every submission arrived as the empty string and was
// rejected with "workspace_name must be at least 2 characters" — an error that
// pointed at a field the handler was not reading, so the typed value looked
// long enough while the check saw none of it.
type createWorkspaceRequest struct {
	Name string `json:"workspace_name"`
}

// createWorkspaceResponse mirrors the shape of PostAuthResult / SwitchResult
// so the frontend can activate the freshly created workspace the same way
// it activates any other.
type createWorkspaceResponse struct {
	WorkspaceID string       `json:"workspace_id"`
	Name        string       `json:"name"`
	Slug        string       `json:"slug"`
	Role        Role         `json:"role"`
	Permissions []Permission `json:"permissions"`
	SessionJWT  string       `json:"session_jwt,omitempty"`
}

// handleCreateWorkspace creates an additional workspace for a user who
// already holds at least one active membership (Req 15.12, 15.13). This is
// distinct from the onboarding-flow workspace creation at
// POST /employer/onboarding/workspace, which provisions the user's very
// first workspace from a Stytch discovery intermediate session.
//
// Creating an *additional* workspace has no discovery intermediate session
// to reuse — the caller already holds a full Stytch session for their
// existing workspace(s), not an intermediate one — so
// CreateOrganizationFromDiscovery (which only accepts an intermediate
// session token) does not apply here. This handler instead: (1) creates a
// plain Stytch Organization with no Member via CreateOrganization, then (2)
// adds the caller as a Member of that Organization via CreateMember.
// WorkspaceService.CreateAdditional then persists the Workspace and owner
// Membership from the resulting organization_id and member_id. Fundis's
// Membership document is authoritative for authorization (Req 16.6), so the
// Stytch Member's RBAC roles are left empty here — they are observability
// only and are never read by the Session_Resolver.
func handleCreateWorkspace(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		var request createWorkspaceRequest
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"invalid request body",
				"",
				"",
				nil,
			)
		}

		if err := ValidateName(request.Name); err != nil {
			return responses.SendErrorWithCode(
				c,
				http.StatusBadRequest,
				"invalid_workspace_name",
				err.Error(),
				"workspace_name",
				"",
				nil,
			)
		}

		user, err := users.GetUserByID(c.Request().Context(), sc.UserID)
		if err != nil || user == nil {
			slog.ErrorContext(c.Request().Context(), "create additional workspace: load user failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"workspace_create_failed",
				"could not create workspace",
				"",
				"try_again",
				nil,
			)
		}

		normalized := NormalizeName(request.Name)
		slug := DeriveSlug(normalized)

		var orgResult stytch.CreateOrganizationResult
		err = cfg.Adapter.Do(c.Request().Context(), "CreateOrganization", false, func(attemptCtx context.Context) error {
			var innerErr error
			orgResult, innerErr = cfg.Adapter.Client().CreateOrganization(attemptCtx, stytch.CreateOrganizationInput{
				Name:                 normalized,
				Slug:                 slug,
				EmailJITProvisioning: "RESTRICTED",
				EmailInvites:         "ALL_ALLOWED",
			})
			return innerErr
		})
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "create additional workspace: stytch organization create failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_provider_unavailable",
				"could not create workspace",
				"",
				"try_again",
				nil,
			)
		}

		var member stytch.Member
		err = cfg.Adapter.Do(c.Request().Context(), "CreateMember", false, func(attemptCtx context.Context) error {
			var innerErr error
			member, innerErr = cfg.Adapter.Client().CreateMember(attemptCtx, stytch.CreateMemberInput{
				OrganizationID: orgResult.OrganizationID,
				Email:          user.Email,
				Name:           strings.TrimSpace(user.FirstName + " " + user.LastName),
			})
			return innerErr
		})
		if err != nil {
			slog.ErrorContext(c.Request().Context(), "create additional workspace: stytch member create failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"auth_provider_unavailable",
				"could not create workspace",
				"",
				"try_again",
				nil,
			)
		}

		result, err := cfg.WorkspaceService.CreateAdditional(
			c.Request().Context(),
			sc.UserID,
			request.Name,
			orgResult.OrganizationID,
			member.MemberID,
		)
		if err != nil {
			var wsErr *WorkspaceError
			if errors.As(err, &wsErr) {
				return responses.SendErrorWithCode(c, wsErr.Status, wsErr.Code, wsErr.Message, "", "", nil)
			}
			slog.ErrorContext(c.Request().Context(), "create additional workspace failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"workspace_create_failed",
				"could not create workspace",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendCreated(c, createWorkspaceResponse{
			WorkspaceID: result.Workspace.ID,
			Name:        result.Workspace.Name,
			Slug:        result.Workspace.Slug,
			Role:        result.Membership.Role,
			Permissions: GrantedPermissions(result.Membership.Role),
		}, "Workspace created")
	}
}

// handleSwitchWorkspace switches the user's active workspace (Req 15.7,
// 15.8, 15.9). SwitchWorkspace needs the caller's raw opaque session
// token — not the resolved SessionContext — because it round-trips Stytch's
// ExchangeSession to mint a new session scoped to the target organization.
func handleSwitchWorkspace(cfg RouteConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := FromEcho(c)
		if !ok {
			return unauthorized(c)
		}

		token, ok := bearerToken(c)
		if !ok {
			return unauthorized(c)
		}

		result, err := cfg.WorkspaceService.SwitchWorkspace(c.Request().Context(), SwitchInput{
			UserID:            sc.UserID,
			TargetWorkspaceID: c.Param("workspaceID"),
			SessionToken:      token,
		})
		if err != nil {
			var membershipErr *MembershipError
			if errors.As(err, &membershipErr) {
				return responses.SendErrorWithCode(c, membershipErr.Status, membershipErr.Code, membershipErr.Message, "", "", nil)
			}
			slog.ErrorContext(c.Request().Context(), "switch workspace failed", "error", err)
			return responses.SendErrorWithCode(
				c,
				http.StatusServiceUnavailable,
				"workspace_switch_failed",
				"could not switch workspace",
				"",
				"try_again",
				nil,
			)
		}

		return responses.SendOK(c, result, "Workspace switched")
	}
}
