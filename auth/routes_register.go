package auth

import (
	"backend/middleware"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	v1 := e.Group("/v1")

	v1.GET("/", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"message": "Welcome to API v1"})
	})

	v1.POST("/register", HandleUserRegistration)
	v1.POST("/login", HandleUserLogin)
	v1.POST("/logout", HandleLogout)
	v1.POST("/refresh", HandleRefresh)
	v1.POST("/me", HandleMe)

	v1.GET("/login/:provider", HandleSocialLogin)
	v1.POST("/login/:provider/callback", HandleSocialLogin)

	v1.GET("/artisan/external-quotation/:quotation/:action", HandleExternalQuotationUpdate)

	v1.GET("/email/verify/:id/:hash", HandleEmailVerify)
	v1.GET("/email/resend", HandleEmailResend)

	v1.GET("/artisans", HandleAllArtisans)

	v1.GET("/profile/:fundis_id", HandleShareUserRecord)

	auth := v1.Group("")
	auth.Use(middleware.JWTAuth)

	auth.GET("/profile", HandleMe)
	auth.POST("/profile", HandleUpdateProfile)
	auth.POST("/profile/professional-background", HandleUpdateProfessionalBackground)
	auth.POST("/profile/legal-document", HandleUpdateLegalDocument)
	auth.POST("/profile/avatar", HandleUpdateAvatar)

	auth.POST("/otp/send", HandleSendOtp)
	auth.POST("/otp/verify", HandleVerifyOtp)
	auth.POST("/otp/resend", HandleResendOtp)

	auth.POST("/forgot-password", HandleForgotPassword)
	auth.POST("/reset-password/:phone", HandleResetPassword)

	auth.PATCH("/fcm-token", HandleUpdateToken)
}

func HandleExternalQuotationUpdate(ctx echo.Context) error {
	return ctx.JSON(200, map[string]string{"status": "ok"})
}

func HandleEmailVerify(ctx echo.Context) error {
	return ctx.JSON(200, map[string]string{"message": "Email verified"})
}

func HandleEmailResend(ctx echo.Context) error {
	return ctx.JSON(200, map[string]string{"message": "Verification email sent"})
}

func HandleUpdateProfessionalBackground(ctx echo.Context) error {
	return ctx.JSON(200, map[string]interface{}{"saved": true, "message": "Record created successfully!"})
}

func HandleUpdateLegalDocument(ctx echo.Context) error {
	return ctx.JSON(200, map[string]interface{}{"saved": true, "message": "File saved successfully!"})
}

func HandleUpdateAvatar(ctx echo.Context) error {
	return ctx.JSON(200, map[string]interface{}{"avatar": true, "message": "Avatar Updated Successfully!"})
}
