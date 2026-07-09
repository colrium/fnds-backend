package users

import (
	"backend/middleware"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	auth := e.Group("")
	auth.Use(middleware.JWTAuth)

	auth.GET("/profile", HandleGetProfile)
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

func HandleUpdateProfessionalBackground(ctx echo.Context) error {
	return ctx.JSON(200, map[string]interface{}{"saved": true, "message": "Record created successfully!"})
}

func HandleUpdateLegalDocument(ctx echo.Context) error {
	return ctx.JSON(200, map[string]interface{}{"saved": true, "message": "File saved successfully!"})
}

func HandleUpdateAvatar(ctx echo.Context) error {
	return ctx.JSON(200, map[string]interface{}{"avatar": true, "message": "Avatar Updated Successfully!"})
}
