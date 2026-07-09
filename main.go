package main

import (
	"fmt"
	"log/slog"
	"os"
	_ "time/tzdata"

	"backend/applications"
	"backend/auth"
	"backend/assessments"
	"backend/banks"
	"backend/configs"
	"backend/courses"
	"backend/institutions"
	"backend/jobrequests"
	"backend/locations"
	"backend/notifications"
	"backend/payments"
	"backend/quotations"
	"backend/reviews"
	"backend/skills"
	"backend/subscriptions"
	"backend/transactions"
	"backend/users"

	_ "github.com/joho/godotenv/autoload"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
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

	api := app.Group("/api")

	auth.RegisterRoutes(api)
	users.RegisterRoutes(api)
	locations.RegisterRoutes(api)
	skills.RegisterRoutes(api)
	banks.RegisterRoutes(api)
	institutions.RegisterRoutes(api)
	jobrequests.RegisterRoutes(api)
	quotations.RegisterRoutes(api)
	reviews.RegisterRoutes(api)
	transactions.RegisterRoutes(api)
	payments.RegisterRoutes(api)
	subscriptions.RegisterRoutes(api)
	courses.RegisterRoutes(api)
	assessments.RegisterRoutes(api)
	notifications.RegisterRoutes(api)
	applications.RegisterRoutes(api)

	port := fmt.Sprintf(":%s", configs.EnvPort())
	slog.Info("server starting", "port", port)

	if err := app.Start(port); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
