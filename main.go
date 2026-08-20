package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"backend/adminapi"
	"backend/applications"
	"backend/assessments"
	"backend/audit"
	"backend/auth"
	"backend/banks"
	"backend/chat"
	"backend/configs"
	"backend/cvparser"
	"backend/documenttypes"
	"backend/files"
	"backend/identity"
	"backend/institutions"
	"backend/integrations"
	"backend/invitations"
	"backend/jobrequests"
	"backend/jobs"
	"backend/locations"
	"backend/logging"
	"backend/middleware"
	"backend/notifications"
	"backend/onboarding"
	"backend/payments"
	"backend/profileapproval"
	"backend/profiles"
	"backend/quotations"
	"backend/ratelimit"
	"backend/referrals"
	"backend/reviews"
	"backend/seeders"
	"backend/skills"
	"backend/stytch"
	"backend/subscriptions"
	"backend/transactions"
	"backend/users"
	"backend/workspaces"

	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

func configureApplicationLoggingMiddleware(app *echo.Echo) {
	app.IPExtractor = echo.ExtractIPDirect()
	app.Use(middleware.RequestContext())
	app.Use(middleware.AccessLog())
	app.Use(echomiddleware.RecoverWithConfig(echomiddleware.RecoverConfig{
		StackSize:         8192,
		DisableStackAll:   true,
		DisablePrintStack: true,
		LogErrorFunc: func(c echo.Context, err error, _ []byte) error {
			panicValue, stack := logging.PanicDetails(err)
			slog.ErrorContext(c.Request().Context(), "panic recovered",
				slog.String("panic", panicValue),
				slog.String("stack", stack),
			)
			return err
		},
	}))
}

type publicArtisanRouteDependencies struct {
	registerAPIRoutes func(*echo.Group)
	shareHandler      echo.HandlerFunc
	sitemapHandler    echo.HandlerFunc
	newLimiter        func() echo.MiddlewareFunc
	newCache          func(middleware.CacheConfig) echo.MiddlewareFunc
}

func newPublicArtisanLimiter() echo.MiddlewareFunc {
	return echomiddleware.RateLimiterWithConfig(echomiddleware.RateLimiterConfig{
		Store: echomiddleware.NewRateLimiterMemoryStoreWithConfig(echomiddleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(10),
			Burst:     30,
			ExpiresIn: 10 * time.Minute,
		}),
	})
}

func registerPublicArtisanRoutes(
	app *echo.Echo,
	api *echo.Group,
	deps publicArtisanRouteDependencies,
) {
	crawlerLimiter := deps.newLimiter()
	app.GET(
		"/artisan/:slug",
		deps.shareHandler,
		crawlerLimiter,
		deps.newCache(middleware.CacheConfig{
			TTL:            5 * time.Minute,
			KeyPrefix:      "artisan-share",
			AppEnvironment: configs.AppEnv(),
		}),
	)
	app.GET(
		"/sitemap-artisans.xml",
		deps.sitemapHandler,
		crawlerLimiter,
		deps.newCache(middleware.CacheConfig{
			TTL:            10 * time.Minute,
			KeyPrefix:      "artisan-sitemap",
			AppEnvironment: configs.AppEnv(),
		}),
	)

	artisanPublicGroup := api.Group("")
	artisanPublicGroup.Use(deps.newLimiter())
	artisanPublicGroup.Use(deps.newCache(middleware.CacheConfig{
		TTL:            5 * time.Minute,
		KeyPrefix:      "artisan-profile",
		AppEnvironment: configs.AppEnv(),
	}))
	deps.registerAPIRoutes(artisanPublicGroup)
}

func main() {
	rawLoggingConfig := logging.RawConfig{
		SourceToken:  configs.BetterStackSourceToken(),
		IngestURL:    configs.BetterStackIngestURL(),
		ServiceName:  configs.BetterStackServiceName(),
		Environment:  configs.AppEnv(),
		MinLevel:     configs.BetterStackMinLevel(),
		BatchSize:    configs.BetterStackBatchSize(),
		FlushSeconds: configs.BetterStackFlushSeconds(),
		QueueSize:    configs.BetterStackQueueSize(),
	}
	loggingConfig, loggingDiagnostics := logging.ResolveConfig(rawLoggingConfig)
	loggingConfig.DiagWriter = os.Stdout
	shipper := logging.NewShipper(loggingConfig)
	shipper.EmitStartupDiagnostics(loggingDiagnostics)
	shipper.Start()

	console := slog.NewJSONHandler(os.Stdout, nil)
	logger := slog.New(logging.NewFanoutHandler(console, shipper.Handler()))
	slog.SetDefault(logger)

	app := echo.New()
	configureApplicationLoggingMiddleware(app)
	app.Use(newCORSMiddleware(configs.AppEnv()))

	// Unauthenticated liveness check for the ALB target group and ECS container
	// healthCheck (both configured in infra to hit this exact path).
	app.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})

	configs.ConnectDB()
	if err := configs.ConnectRedis(); err != nil {
		slog.Warn("Redis unavailable; continuing with caching disabled", "error", err)
	}
	slog.Info("database connected", "env", configs.AppEnv())

	// Validate required Stytch configuration when employer auth is enabled.
	if workspaces.IsStytchAuthEnabled() {
		required := []struct {
			name  string
			value string
		}{
			{"STYTCH_PROJECT_ID", configs.StytchProjectID()},
			{"STYTCH_SECRET", configs.StytchSecret()},
			{"STYTCH_PUBLIC_TOKEN", configs.StytchPublicToken()},
			{"STYTCH_ENV", configs.StytchEnvironment()},
			{"STYTCH_WEBHOOK_SECRET", configs.StytchWebhookSecret()},
		}
		for _, v := range required {
			if strings.TrimSpace(v.value) == "" {
				slog.Error("required Stytch configuration variable is empty", "variable", v.name)
				os.Exit(1)
			}
		}
	}

	notificationCacheSettings, err := configs.LoadNotificationCacheSettings()
	if err != nil {
		slog.Error("invalid notification cache configuration", "error", err)
		os.Exit(1)
	}
	if err := notifications.ConfigureDefaultStore(
		notifications.NewMongoStore(),
		notificationCacheSettings,
		notifications.NewSlogCacheObserver(),
	); err != nil {
		slog.Error("failed to configure notifications default store", "error", err)
		os.Exit(1)
	}

	maxUploadBytes, err := configs.AWSS3MaxUploadBytes()
	if err != nil {
		slog.Error("invalid AWS_S3_MAX_UPLOAD_BYTES", "error", err)
		os.Exit(1)
	}
	s3Store, err := files.NewS3Store(context.Background(), files.S3Config{
		Region:        configs.AWSS3Region(),
		Bucket:        configs.AWSS3Bucket(),
		PublicBaseURL: configs.AWSS3PublicBaseURL(),
	})
	if err != nil {
		slog.Error("failed to configure S3 storage", "error", err)
		os.Exit(1)
	}
	fileHandler := files.NewHandler(files.NewMongoRepository(), s3Store, maxUploadBytes)
	avatarHandler := users.NewAvatarHandler(&users.MongoAvatarRepository{}, s3Store)

	// CV parser: instantiate the Bedrock-backed parser and handler.
	bedrockClient, err := cvparser.NewBedrockClient(context.Background(), configs.AWSRegion())
	if err != nil {
		slog.Error("failed to configure Bedrock client", "error", err)
		os.Exit(1)
	}
	cvParser := cvparser.NewBedrockParser(bedrockClient, configs.BedrockModelID())
	cvMapper := cvparser.NewMapper()
	cvHandler := cvparser.NewHandler(files.NewMongoRepository(), s3Store, cvParser, cvMapper)

	// Idempotent: each seeder skips when its collection is already populated.
	seeders.RunAll()

	if err := users.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure user indexes", "error", err)
		os.Exit(1)
	}
	if err := files.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure file indexes", "error", err)
		os.Exit(1)
	}
	if err := subscriptions.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure subscription indexes", "error", err)
		os.Exit(1)
	}
	if err := quotations.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure quotation indexes", "error", err)
		os.Exit(1)
	}
	if err := referrals.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure referral indexes", "error", err)
		os.Exit(1)
	}
	if err := notifications.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure notification indexes", "error", err)
		os.Exit(1)
	}
	if err := jobrequests.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure job request indexes", "error", err)
		os.Exit(1)
	}
	if err := institutions.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure institution indexes", "error", err)
		os.Exit(1)
	}
	if err := workspaces.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to create workspace indexes", "error", err)
		os.Exit(1)
	}
	if err := workspaces.EnsureWebhookIndexes(context.Background()); err != nil {
		slog.Error("failed to create webhook indexes", "error", err)
		os.Exit(1)
	}
	if err := workspaces.EnsureMigrationIndexes(context.Background()); err != nil {
		slog.Error("failed to create migration indexes", "error", err)
		os.Exit(1)
	}
	if err := workspaces.EnsureInvoiceIndexes(context.Background()); err != nil {
		slog.Error("failed to create invoice indexes", "error", err)
		os.Exit(1)
	}
	if err := identity.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to create identity indexes", "error", err)
		os.Exit(1)
	}
	if err := invitations.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to create invitation indexes", "error", err)
		os.Exit(1)
	}
	if err := onboarding.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to create onboarding indexes", "error", err)
		os.Exit(1)
	}
	if err := audit.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to create audit indexes", "error", err)
		os.Exit(1)
	}
	if err := assessments.EnsureIndexes(context.Background()); err != nil {
		slog.Error("failed to ensure assessment indexes", "error", err)
		os.Exit(1)
	}
	go notifications.NewDefaultDispatcher().Run(context.Background())
	scheduler := jobs.NewScheduler()

	// ─── Construct Stytch adapter and workspace-related components (Task 21.1) ───
	// The adapter and services are built regardless of the feature flag so that
	// the health endpoint, migration endpoints, and route registry verification
	// are always available. The feature flag gates whether sessions resolve through
	// Stytch, not whether the components exist.
	stytchClock := stytch.SystemClock()

	stytchConfig := stytch.Config{
		ProjectID:     configs.StytchProjectID(),
		Secret:        configs.StytchSecret(),
		PublicToken:   configs.StytchPublicToken(),
		Environment:   configs.StytchEnvironment(),
		WebhookSecret: configs.StytchWebhookSecret(),
		Clock:         stytchClock,
	}

	var stytchAdapter *stytch.Adapter
	if workspaces.IsStytchAuthEnabled() {
		httpClient, err := stytch.NewHTTPClient(stytchConfig)
		if err != nil {
			slog.Error("failed to construct stytch http client", "error", err)
			os.Exit(1)
		}
		stytchAdapter = stytch.NewWithJWKSFetch(stytchConfig, httpClient, stytch.FetchJWKS(stytchConfig))
	} else {
		// Employer Stytch auth is disabled: the components still need to
		// exist (health endpoint, migration report, route registry
		// verification), but nothing calls out to Stytch, so the in-memory
		// fake is safe here — it is never reached by a real request.
		stytchAdapter = stytch.NewWithClient(stytchConfig, stytch.NewFake(stytchClock))
	}

	auditLogger := audit.NewLogger()
	rateLimiter := ratelimit.NewSlidingWindowLimiter(configs.RDB, stytchClock)

	// Probe transaction support (Req 14.3, 14.4). A probe failure does not mean
	// transactions are unsupported — it can also mean a transient network blip
	// or primary stepdown. Treating it as "unsupported" would silently disable
	// atomic operations for ownership transfer and membership changes, so a
	// failed probe fails startup instead of guessing.
	txSupported, txErr := configs.TransactionsSupported(context.Background())
	if txErr != nil {
		slog.Error("transaction probe failed", "error", txErr)
		os.Exit(1)
	}
	slog.Info("transaction support probed", "supported", txSupported)

	workspaceService := workspaces.NewWorkspaceService(stytchAdapter, auditLogger, stytchClock, rateLimiter)
	membershipService := workspaces.NewMembershipService(stytchAdapter, auditLogger, stytchClock, txSupported)
	invitationService := invitations.NewService(rateLimiter, auditLogger, stytchClock, membershipService)
	invitationHandlers := invitations.NewHTTPHandlers(invitations.HandlersConfig{
		Service:          invitationService,
		Adapter:          stytchAdapter,
		WorkspaceService: workspaceService,
	})
	onboardingService := onboarding.NewService(stytchAdapter, rateLimiter)
	webhookConsumer := workspaces.NewWebhookConsumer(stytchAdapter, stytchClock, membershipService, workspaceService, auditLogger)
	healthHandler := workspaces.NewHealthHandler(stytchAdapter, txSupported)
	migrationJob := workspaces.NewMigrationJob(workspaceService, auditLogger, stytchClock)
	migrationHandler := workspaces.NewMigrationHandler(migrationJob)
	routeRegistry := workspaces.NewRouteRegistry()

	// Verify the permission matrix at startup (Req 7.3, 7.13).
	if err := workspaces.VerifyMatrix(); err != nil {
		slog.Error("permission matrix verification failed", "error", err)
		os.Exit(1)
	}

	// Register workspace-related routes (Task 13.5, 21.1).
	workspaces.RegisterRoutes(workspaces.RouteConfig{
		App:               app,
		Adapter:           stytchAdapter,
		AuditLogger:       auditLogger,
		RateLimiter:       rateLimiter,
		WorkspaceService:  workspaceService,
		MembershipService: membershipService,
		Invitations:       invitationHandlers,
		OnboardingService: onboardingService,
		WebhookConsumer:   webhookConsumer,
		HealthHandler:     healthHandler,
		MigrationHandler:  migrationHandler,
		Registry:          routeRegistry,
		Clock:             stytchClock,
	})

	// Verify route registry (Req 8.6): all declared workspace-scoped routes
	// match workspaceScopedRoutes and vice versa; fails startup on drift.
	if err := routeRegistry.Verify(); err != nil {
		slog.Error("route registry verification failed", "error", err)
		os.Exit(1)
	}

	api := app.Group("/api/v1")

	api.GET("/", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Welcome to API v1"})
	})

	// Crawlable, server-rendered vacancy share page (no /api/v1 prefix so the URL
	// is clean for sharing). Humans clicking "Apply" are deep-linked into the SPA.
	app.GET("/vacancy/:id", jobrequests.HandleVacancySharePage)

	// Public artisan pages and JSON API share cache policy but use separate
	// crawler and API limiter instances.
	registerPublicArtisanRoutes(app, api, publicArtisanRouteDependencies{
		registerAPIRoutes: profiles.RegisterPublicRoutes,
		shareHandler:      profiles.HandleArtisanSharePage,
		sitemapHandler:    profiles.HandleArtisanSitemap,
		newLimiter:        newPublicArtisanLimiter,
		newCache:          middleware.RedisCache,
	})

	auth.RegisterPublicRoutes(api)
	// Rate-limit password-reset endpoints: they send SMS and are brute-forceable.
	// Note: IPExtractor set to ExtractIPDirect() for security. If fronting LB is added,
	// switch to ExtractIPFromXFFHeader with LB CIDR trusted.
	resetGroup := api.Group("")
	resetGroup.Use(echomiddleware.RateLimiterWithConfig(echomiddleware.RateLimiterConfig{
		Store: echomiddleware.NewRateLimiterMemoryStoreWithConfig(echomiddleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(1),
			Burst:     3,
			ExpiresIn: 3 * time.Minute,
		}),
	}))
	users.RegisterPublicRoutes(resetGroup)
	applications.RegisterRoutes(api)
	locations.RegisterRoutes(api)
	skills.RegisterRoutes(api)
	banks.RegisterRoutes(api)
	institutions.RegisterRoutes(api)
	payments.RegisterRoutes(api)
	quotations.RegisterPaymentHooks()
	subscriptions.RegisterPaymentHooks()
	reviews.RegisterPublicRoutes(api)
	quotations.RegisterPublicRoutes(api)
	jobrequests.RegisterPublicRoutes(api)

	// Unauthenticated and enumerable by code, so it gets its own limiter instance
	// rather than sharing the public-artisan one: exhausting one must not lock out
	// the other.
	assessments.RegisterCertificatePublicRoutes(api, echomiddleware.RateLimiterWithConfig(
		echomiddleware.RateLimiterConfig{
			Store: echomiddleware.NewRateLimiterMemoryStoreWithConfig(
				echomiddleware.RateLimiterMemoryStoreConfig{
					Rate:      rate.Limit(5),
					Burst:     20,
					ExpiresIn: 10 * time.Minute,
				},
			),
		},
	))
	documenttypes.RegisterRoutes(api)
	users.RegisterAvatarPublicRoutes(api, avatarHandler)

	api.GET("/artisans", auth.HandleAllArtisans)
	api.GET("/profile/:fundis_id", auth.HandleShareUserRecord)
	api.GET("/email/verify/:id/:hash", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Email verified"})
	})

	authGroup := api.Group("")
	authGroup.Use(middleware.JWTAuth)

	profiles.RegisterAuthRoutes(authGroup)
	profileapproval.RegisterAuthRoutes(authGroup)
	users.RegisterAuthRoutes(authGroup)
	reviews.RegisterAuthRoutes(authGroup)
	jobrequests.RegisterAuthRoutes(authGroup)
	quotations.RegisterAuthRoutes(authGroup)
	quotations.RegisterAttachmentRoutes(authGroup, s3Store, maxUploadBytes)
	transactions.RegisterAuthRoutes(authGroup)
	subscriptions.RegisterAuthRoutes(authGroup)
	referrals.RegisterAuthRoutes(authGroup)
	chat.RegisterAuthRoutes(authGroup)
	notificationStore, err := notifications.DefaultStore()
	if err != nil {
		slog.Error("failed to resolve notifications default store", "error", err)
		os.Exit(1)
	}
	notifications.RegisterAuthRoutes(authGroup, notifications.NewHandler(notificationStore))
	banks.RegisterAuthRoutes(authGroup)
	documenttypes.RegisterAuthRoutes(authGroup)
	users.RegisterAvatarAuthRoutes(authGroup, avatarHandler)

	// Artisan-only routes. These keep the bare /api/v1 prefix because their paths
	// are already unique and are in use by shipped clients (/cv/parse,
	// /job-vacancies/..., /artisan/availability).
	artisanGroup := api.Group("")
	artisanGroup.Use(middleware.JWTAuth, middleware.IsArtisan)
	users.RegisterArtisanRoutes(artisanGroup)
	jobrequests.RegisterArtisanRoutes(artisanGroup)
	files.RegisterArtisanRoutes(artisanGroup, fileHandler)
	artisanGroup.POST("/cv/parse", cvHandler.ParseCV)

	// The three assessment role surfaces are namespaced by role. Several
	// assessment paths (/assessments/appointments, /assessments/centres,
	// /assessments/practical-tasks) exist for more than one role with different
	// handlers; without distinct prefixes they collide in echo's router and the
	// last group registered silently wins for the whole method+path, so callers
	// of the shadowed route get that group's middleware and 401 instead of their
	// own handler.
	// Built once and shared: the service caches its OAuth access token, so a
	// per-request instance would refresh on every booking and risk Zoho's
	// refresh rate limit. A missing or partial configuration logs once here and
	// leaves meetings disabled rather than failing each booking at runtime.
	var meetingCreator assessments.MeetingCreator
	if zohoService, err := integrations.NewZohoMeetingService(); err != nil {
		if errors.Is(err, integrations.ErrZohoNotConfigured) {
			slog.Warn("zoho meeting disabled; oral bookings will have no video link", "reason", err)
		} else {
			slog.Error("zoho meeting misconfigured; oral bookings will have no video link", "error", err)
		}
	} else {
		meetingCreator = assessments.NewZohoMeetingAdapter(zohoService)
		slog.Info("zoho meeting enabled")
	}

	artisanAssessmentGroup := api.Group("/artisan")
	artisanAssessmentGroup.Use(middleware.JWTAuth, middleware.IsArtisan)
	assessments.RegisterArtisanRoutes(artisanAssessmentGroup, meetingCreator)

	// Assessor-only routes
	assessorGroup := api.Group("/assessor")
	assessorGroup.Use(middleware.JWTAuth, middleware.IsAssessor)
	assessments.RegisterAssessorRoutes(assessorGroup)

	// Admin-only routes
	adminGroup := api.Group("/admin")
	adminGroup.Use(middleware.JWTAuth, middleware.IsAdmin)
	assessments.RegisterAdminRoutes(adminGroup)
	auth.RegisterAdminRoutes(adminGroup)
	adminapi.RegisterAdminRoutes(adminGroup)

	authGroup.GET("/email/resend", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Verification email sent"})
	})

	// Retention is operator-configurable and validated against the statutory
	// minimums (Req 12.10, 23.6). A bad value fails startup rather than
	// silently deleting records the requirements still cover.
	retentionConfig, err := workspaces.RetentionConfigFromEnv()
	if err != nil {
		slog.Error("invalid workspace retention configuration", "error", err)
		os.Exit(1)
	}

	// Register the workspace background jobs (invitation expiry sweep,
	// deletion effector, abandoned-onboarding reaper, zero-owner detector,
	// Stytch member-delete retry, retention purge) before starting the
	// scheduler's tickers.
	schedulerJobs := workspaces.NewSchedulerJobs(
		invitationService.SweepExpired,
		workspaceService,
		membershipService,
		stytchAdapter,
		stytchClock,
		retentionConfig,
	)
	scheduler.RegisterHourly(schedulerJobs.SweepExpiredInvitations)
	scheduler.RegisterHourly(schedulerJobs.EffectDueDeletions)
	scheduler.RegisterHourly(schedulerJobs.ReapAbandonedOnboarding)
	scheduler.RegisterHourly(schedulerJobs.ReportZeroOwnerWorkspaces)
	scheduler.RegisterFrequent(schedulerJobs.RetryPendingStytchDeletes)
	scheduler.RegisterDaily(schedulerJobs.PurgeExpiredRecords)

	// Start the background job scheduler with hourly, 15-minute, and daily tickers.
	scheduler.Start()

	port := fmt.Sprintf(":%s", configs.EnvPort())
	slog.Info("server starting", "port", port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- app.Start(port)
	}()

	serverExited := false
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		serverExited = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := app.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		slog.Error("graceful shutdown failed", "error", shutdownErr)
	}

	if !serverExited && shutdownErr == nil {
		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
		}
	}

	scheduler.Stop()
	shipper.Close()
}
