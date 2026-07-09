package users

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/otp/send", HandleSendOtp)
	g.POST("/otp/verify", HandleVerifyOtp)
	g.POST("/otp/resend", HandleResendOtp)
	g.POST("/forgot-password", HandleForgotPassword)
	g.POST("/reset-password/:phone", HandleResetPassword)
	g.PATCH("/fcm-token", HandleUpdateToken)
}

func HandleSendOtp(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid user")
	}

	if user.PhoneVerified {
		return responses.SendError(ctx, 400, "OTP already verified")
	}

	otp := "123456"
	_ = UpdateUser(userID, bson.M{"otp": otp})

	return responses.SendOK(ctx, nil, "OTP sent successfully")
}

func HandleVerifyOtp(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	otp := ctx.FormValue("otp")
	if otp == "" {
		return responses.SendError(ctx, 400, "otp is required")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid user")
	}

	if user.PhoneVerified {
		return responses.SendError(ctx, 400, "OTP already verified")
	}

	if user.Otp != otp {
		return responses.SendError(ctx, 400, "Invalid OTP")
	}

	_ = UpdateUser(userID, bson.M{"otp": "", "phone_verified": true})

	return responses.SendOK(ctx, nil, "OTP verified successfully")
}

func HandleResendOtp(ctx echo.Context) error {
	return HandleSendOtp(ctx)
}

func HandleForgotPassword(ctx echo.Context) error {
	phone := ctx.FormValue("phone")
	userType := ctx.FormValue("user_type")

	if phone == "" {
		return responses.SendError(ctx, 400, "phone is required")
	}
	if userType == "" {
		return responses.SendError(ctx, 400, "user_type is required")
	}

	user, err := GetUserByPhoneAndType(phone, userType)
	if err != nil {
		return responses.SendError(ctx, 400, "Phone number does not exist")
	}

	otp := "123456"
	_ = UpdateUser(user.ID, bson.M{"otp": otp})

	return responses.SendOK(ctx, nil, "OTP sent successfully")
}

func HandleResetPassword(ctx echo.Context) error {
	phone := ctx.Param("phone")
	otp := ctx.FormValue("otp")
	password := ctx.FormValue("password")
	confirmPassword := ctx.FormValue("confirm_password")
	userType := ctx.FormValue("user_type")

	if phone == "" || otp == "" || password == "" || confirmPassword == "" || userType == "" {
		return responses.SendError(ctx, 400, "All fields are required")
	}

	if password != confirmPassword {
		return responses.SendError(ctx, 400, "Passwords do not match")
	}

	user, err := GetUserByPhoneAndType(phone, userType)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid phone number")
	}

	if user.Otp != otp {
		return responses.SendError(ctx, 400, "Invalid OTP")
	}

	_ = UpdateUser(user.ID, bson.M{"password": []byte(password), "otp": "", "phone_verified": true})

	return responses.SendOK(ctx, nil, "Password reset successfully")
}

func HandleUpdateToken(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	token := ctx.FormValue("token")
	if token == "" {
		return responses.SendError(ctx, 400, "token is required")
	}

	_ = UpdateUser(userID, bson.M{"fcm_token": token})

	return responses.SendOK(ctx, nil, "FCM token updated")
}
