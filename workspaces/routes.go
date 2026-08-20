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
	InvitationService any // *invitations.Service — typed as any to avoid import cycle
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
					"",
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
					"",
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

			postAuth, postAuthErr := cfg.WorkspaceService.ResolvePostAuth(c.Request().Context(), result.UserID, result.WorkspaceID)
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

	// Invitation public endpoints.
	api.GET("/invitations/:token", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}, cfg.RateLimiter.Guard(ratelimit.LimitInviteResolveMin, ratelimit.SubjectIP))

	api.POST("/invitations/:token/decline", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}, cfg.RateLimiter.Guard(ratelimit.LimitInviteResolveMin, ratelimit.SubjectIP))

	api.POST("/invitations/:token/accept", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}, cfg.RateLimiter.Guard(ratelimit.LimitInviteResolveMin, ratelimit.SubjectIP))

	// Webhook endpoint — registered before body-consuming middleware. Public
	// and unauthenticated, so it needs IP-based limiting like any other
	// unauthenticated endpoint to bound the cost of garbage POSTs.
	api.POST("/stytch/webhooks", cfg.WebhookConsumer.Handle, cfg.RateLimiter.Guard(ratelimit.LimitWebhookPerIP, ratelimit.SubjectIP))

	// Health endpoint — unauthenticated, no secrets.
	api.GET("/health/identity", cfg.HealthHandler.HandleHealth)

	// ─── 2. Onboarding-ticket scoped routes ──────────────────────────
	onboardingGroup := api.Group("/employer/onboarding")
	onboardingGroup.GET("/state", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
	onboardingGroup.POST("/name", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
	onboardingGroup.POST("/workspace", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
	onboardingGroup.POST("/link/decision", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
	onboardingGroup.POST("/link/challenge", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
	onboardingGroup.POST("/link/challenge/verify", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
	onboardingGroup.POST("/sign-out", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})

	// ─── 3. Session-scoped routes (authenticated, no workspace required) ───
	// These are excluded from the workspace route registry by name.
	sessionGroup := api.Group("/workspaces")
	sessionGroup.GET("", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}, resolver.Authenticate)
	sessionGroup.POST("", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}, resolver.Authenticate)
	sessionGroup.POST("/:workspaceID/switch", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}, resolver.Authenticate)

	// ─── 4. Workspace-scoped routes (ScopedGroup) ────────────────────
	// Every route here declares a Permission at compile time (Req 8.5).
	handler := func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true, "workspace_id": contextutil.GetWorkspaceID(c)})
	}

	wsSG := NewScopedGroup(api, "/workspaces/:workspaceID", resolver, cfg.Registry)
	wsSG.GET("", PermWorkspaceView, handler)
	wsSG.PATCH("", PermWorkspaceRename, handler)
	wsSG.POST("/deletion", PermWorkspaceDelete, handler)
	wsSG.DELETE("/deletion", PermWorkspaceDelete, handler)
	wsSG.GET("/permissions", PermWorkspaceView, handler)
	wsSG.POST("/import-personal-data", PermWorkspaceImportPersonalData, handler)
	wsSG.GET("/members", PermMemberView, handler)
	wsSG.PATCH("/members/:membershipID", PermMemberRoleChange, handler)
	wsSG.DELETE("/members/:membershipID", PermMemberRemove, handler)
	wsSG.POST("/members/:membershipID/suspension", PermMemberRemove, handler)
	wsSG.DELETE("/members/:membershipID/suspension", PermMemberRemove, handler)
	wsSG.POST("/ownership", PermOwnershipTransfer, handler)
	wsSG.GET("/invitations", PermMemberView, handler)
	wsSG.POST("/invitations", PermMemberInvite, handler)
	wsSG.POST("/invitations/:id/resend", PermMemberInvite, handler)
	wsSG.DELETE("/invitations/:id", PermMemberInvite, handler)
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
	adminGroup.POST("/workspaces/:workspaceID/ownership", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	})
}
