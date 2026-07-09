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
	"backend/configs"
	"backend/contextutil"
	"backend/courses"
	"backend/institutions"
	"backend/jobrequests"
	"backend/locations"
	"backend/middleware"
	"backend/notifications"
	"backend/payments"
	"backend/quotations"
	"backend/responses"
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

	api := app.Group("/api/v1")

	// Public routes
	api.GET("/", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Welcome to API v1"})
	})

	// Auth routes
	api.POST("/register", auth.HandleUserRegistration)
	api.POST("/login", auth.HandleUserLogin)
	api.POST("/logout", auth.HandleLogout)
	api.POST("/refresh", auth.HandleRefresh)
	api.POST("/me", auth.HandleMe)
	api.GET("/login/:provider", auth.HandleSocialLogin)
	api.POST("/login/:provider/callback", auth.HandleSocialLogin)
	api.GET("/artisans", auth.HandleAllArtisans)
	api.GET("/profile/:fundis_id", auth.HandleShareUserRecord)

	// Startup/shutdown
	api.POST("/startup", applications.HandleStartup)

	// Public lookup routes
	api.GET("/locations", locations.HandleGetLocations)
	api.GET("/request-types", skills.HandleGetSkills)
	api.GET("/banks", banks.HandleGetBanks)
	api.GET("/institution/all", institutions.HandleGetAll)
	api.GET("/institution/:institutionId", institutions.HandleGetOne)
	api.POST("/institution/create", institutions.HandleCreate)
	api.PATCH("/institution/:institutionId", institutions.HandleUpdate)

	// Public webhook routes
	api.POST("/flutterwave/webhook", payments.HandleFlutterwaveWebhook)
	api.POST("/cellulant/webhook", payments.HandleCellulantWebhook)
	api.POST("/cellulant/payment-link/webhook", payments.HandleCellulantPaymentLinkWebhook)
	api.GET("/kopokopo/webhook", payments.HandleKopoKopoWebhook)
	api.POST("/kopokopo/webhook", payments.HandleKopoKopoWebhook)

	// Public notification route
	api.POST("/notify/artisan", notifications.HandleSendNotification)

	// Public assessment routes
	api.GET("/assessments", assessments.HandleGetAssessments)
	api.GET("/assessments/types", assessments.HandleGetAssessmentTypes)

	// Course routes
	api.GET("/courses", courses.HandleGetCourses)
	api.GET("/courses/categories", courses.HandleGetCategories)
	api.GET("/courses/:courseId/topics", courses.HandleGetTopics)
	api.GET("/courses/topics/:topicId/lessons", courses.HandleGetLessons)

	// Auth-protected routes
	authGroup := api.Group("")
	authGroup.Use(middleware.JWTAuth)

	// Profile routes
	authGroup.GET("/profile", users.HandleGetProfile)
	authGroup.POST("/profile", users.HandleUpdateProfile)
	authGroup.POST("/profile/professional-background", users.HandleUpdateProfessionalBackground)
	authGroup.POST("/profile/legal-document", users.HandleUpdateLegalDocument)
	authGroup.POST("/profile/avatar", users.HandleUpdateAvatar)

	// OTP routes
	authGroup.POST("/otp/send", users.HandleSendOtp)
	authGroup.POST("/otp/verify", users.HandleVerifyOtp)
	authGroup.POST("/otp/resend", users.HandleResendOtp)

	// Password routes
	authGroup.POST("/forgot-password", users.HandleForgotPassword)
	authGroup.POST("/reset-password/:phone", users.HandleResetPassword)

	// FCM token
	authGroup.PATCH("/fcm-token", users.HandleUpdateToken)

	// Review routes
	authGroup.POST("/reviews", func(c echo.Context) error {
		userID := contextutil.GetUserID(c)
		if userID == "" {
			return responses.SendError(c, 401, "Unauthorized")
		}
		var review reviews.Review
		if err := c.Bind(&review); err != nil {
			return responses.SendError(c, 400, "Invalid request body")
		}
		review.CustomerID = userID
		if err := reviews.CreateReview(&review); err != nil {
			return responses.SendError(c, 500, "Failed to create review")
		}
		return responses.SendCreated(c, review, "Review created successfully")
	})
	authGroup.GET("/reviews", func(c echo.Context) error {
		userID := contextutil.GetUserID(c)
		if userID == "" {
			return responses.SendError(c, 401, "Unauthorized")
		}
		reviewList, err := reviews.ListReviews(map[string]interface{}{"customer_id": userID})
		if err != nil {
			return responses.SendError(c, 500, "Failed to fetch reviews")
		}
		return responses.SendOK(c, reviewList, "Reviews retrieved successfully")
	})

	// Job request routes
	jobrequests.RegisterAuthRoutes(authGroup)

	// Quotation routes
	quotations.RegisterAuthRoutes(authGroup)

	// Transaction routes
	transactions.RegisterAuthRoutes(authGroup)

	// Subscription routes
	subscriptions.RegisterAuthRoutes(authGroup)

	// Assessment auth routes
	assessments.RegisterAuthRoutes(authGroup)

	// Email verification
	authGroup.GET("/email/resend", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Verification email sent"})
	})
	api.GET("/email/verify/:id/:hash", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Email verified"})
	})

	port := fmt.Sprintf(":%s", configs.EnvPort())
	slog.Info("server starting", "port", port)

	if err := app.Start(port); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
