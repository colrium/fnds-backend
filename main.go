package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
	_ "time/tzdata"

	"backend/applications"
	"backend/auth"
	"backend/banks"
	"backend/chat"
	"backend/configs"
	"backend/documenttypes"
	"backend/files"
	"backend/institutions"
	"backend/jobrequests"
	"backend/locations"
	"backend/middleware"
	"backend/notifications"
	"backend/payments"
	"backend/profiles"
	"backend/quotations"
	"backend/referrals"
	"backend/reviews"
	"backend/seeders"
	"backend/skills"
	"backend/subscriptions"
	"backend/transactions"
	"backend/users"

	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

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
			TTL:       5 * time.Minute,
			KeyPrefix: "artisan-share",
		}),
	)
	app.GET(
		"/sitemap-artisans.xml",
		deps.sitemapHandler,
		crawlerLimiter,
		deps.newCache(middleware.CacheConfig{
			TTL:       10 * time.Minute,
			KeyPrefix: "artisan-sitemap",
		}),
	)

	artisanPublicGroup := api.Group("")
	artisanPublicGroup.Use(deps.newLimiter())
	artisanPublicGroup.Use(deps.newCache(middleware.CacheConfig{
		TTL:       5 * time.Minute,
		KeyPrefix: "artisan-profile",
	}))
	deps.registerAPIRoutes(artisanPublicGroup)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	app := echo.New()
	app.IPExtractor = echo.ExtractIPDirect()
	app.Use(echomiddleware.Logger())
	app.Use(echomiddleware.Recover())
	app.Use(newCORSMiddleware(configs.AppEnv()))

	configs.ConnectDB()
	if err := configs.ConnectRedis(); err != nil {
		slog.Error("failed to initialize Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("database connected", "env", configs.AppEnv())

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
		Region:        configs.AWSRegion(),
		Bucket:        configs.AWSS3Bucket(),
		PublicBaseURL: configs.AWSS3PublicBaseURL(),
	})
	if err != nil {
		slog.Error("failed to configure S3 storage", "error", err)
		os.Exit(1)
	}
	fileHandler := files.NewHandler(files.NewMongoRepository(), s3Store, maxUploadBytes)
	avatarHandler := users.NewAvatarHandler(&users.MongoAvatarRepository{}, s3Store)

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
	go notifications.NewDefaultDispatcher().Run(context.Background())

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

	documenttypes.RegisterRoutes(api)
	users.RegisterAvatarPublicRoutes(api, avatarHandler)

	api.GET("/artisans", auth.HandleAllArtisans)
	api.GET("/profile/:fundis_id", auth.HandleShareUserRecord)
	api.GET("/email/verify/:id/:hash", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Email verified"})
	})

	authGroup := api.Group("")
	authGroup.Use(middleware.JWTAuth)

	auth.RegisterAuthRoutes(authGroup)
	profiles.RegisterAuthRoutes(authGroup)
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

	// Artisan-only routes
	artisanGroup := api.Group("")
	artisanGroup.Use(middleware.JWTAuth, middleware.IsArtisan)
	users.RegisterArtisanRoutes(artisanGroup)
	jobrequests.RegisterArtisanRoutes(artisanGroup)
	files.RegisterArtisanRoutes(artisanGroup, fileHandler)

	authGroup.GET("/email/resend", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Verification email sent"})
	})

	port := fmt.Sprintf(":%s", configs.EnvPort())
	slog.Info("server starting", "port", port)

	if err := app.Start(port); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
