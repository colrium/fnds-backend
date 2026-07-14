package main

import (
	"fmt"
	"log/slog"
	"os"
	_ "time/tzdata"

	"backend/applications"
	"backend/assessments"
	"backend/auth"
	"backend/banks"
	"backend/chat"
	"backend/configs"
	"backend/courses"
	"backend/documenttypes"
	"backend/insurance"
	"backend/institutions"
	"backend/jobrequests"
	"backend/locations"
	"backend/middleware"
	"backend/notifications"
	"backend/payments"
	"backend/profiles"
	"backend/quotations"
	"backend/reviews"
	"backend/skills"
	"backend/subscriptions"
	"backend/transactions"
	"backend/users"

	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	app := echo.New()
	app.Use(echomiddleware.Logger())
	app.Use(echomiddleware.Recover())
	app.Use(echomiddleware.CORS())

	configs.ConnectDB()
	slog.Info("database connected", "env", configs.AppEnv())

	api := app.Group("/api/v1")

	api.GET("/", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Welcome to API v1"})
	})

	auth.RegisterPublicRoutes(api)
	// Rate-limit password-reset endpoints: they send SMS and are brute-forceable.
	resetGroup := api.Group("")
	resetGroup.Use(echomiddleware.RateLimiterWithConfig(echomiddleware.RateLimiterConfig{
		Store: echomiddleware.NewRateLimiterMemoryStore(rate.Limit(1)), // ~1 req/sec/IP, small burst
	}))
	users.RegisterPublicRoutes(resetGroup)
	applications.RegisterRoutes(api)
	locations.RegisterRoutes(api)
	skills.RegisterRoutes(api)
	banks.RegisterRoutes(api)
	institutions.RegisterRoutes(api)
	payments.RegisterRoutes(api)
	notifications.RegisterPublicRoutes(api)
	assessments.RegisterPublicRoutes(api)
	courses.RegisterRoutes(api)
	reviews.RegisterPublicRoutes(api)
	quotations.RegisterPublicRoutes(api)
	documenttypes.RegisterRoutes(api)

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
	transactions.RegisterAuthRoutes(authGroup)
	subscriptions.RegisterAuthRoutes(authGroup)
	assessments.RegisterAuthRoutes(authGroup)
	insurance.RegisterAuthRoutes(authGroup)
	chat.RegisterAuthRoutes(authGroup)
	banks.RegisterAuthRoutes(authGroup)
	documenttypes.RegisterAuthRoutes(authGroup)

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
