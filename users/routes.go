package users

import (
	"log"

	"backend/contextutil"
	"backend/notifications"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/crypto/bcrypt"
)

type verifyOtpRequest struct {
	Otp string `json:"otp"`
}
type forgotPasswordRequest struct {
	Phone    string `json:"phone"`
	UserType string `json:"user_type"`
}
type resetPasswordRequest struct {
	Otp             string `json:"otp"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
	UserType        string `json:"user_type"`
}

// issuePhoneOTP generates a fresh code, persists it, and sends it by SMS.
// SMS delivery failures are logged but do not fail the request, so the user
// can still retry via resend without the account being wedged.
func issuePhoneOTP(userID, phone string) {
	otp := GenerateOTP()
	if err := UpdateUser(userID, bson.M{"otp": otp}); err != nil {
		log.Printf("[otp] failed to persist code for user %s: %v", userID, err)
		return
	}
	if err := notifications.SendOTPSMS(phone, otp); err != nil {
		log.Printf("[otp] failed to send SMS to %s: %v", phone, err)
	}
}

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/otp/send", HandleSendOtp)
	g.POST("/otp/verify", HandleVerifyOtp)
	g.POST("/otp/resend", HandleResendOtp)
	g.PATCH("/fcm-token", HandleUpdateToken)
}

func RegisterPublicRoutes(g *echo.Group) {
	g.POST("/forgot-password", HandleForgotPassword)
	g.POST("/reset-password/:phone", HandleResetPassword)
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

	issuePhoneOTP(user.ID, user.Phone)

	return responses.SendOK(ctx, nil, "OTP sent successfully")
}

func HandleVerifyOtp(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req verifyOtpRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	otp := req.Otp

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

	err = UpdateUser(userID, bson.M{
		"otp":            "",
		"phone_verified": true,
		"status":         StatusActive,
		"is_active":      true,
	})
	if err != nil {
		return responses.SendError(ctx, 500, "failed to verify OTP")
	}

	return responses.SendOK(ctx, nil, "OTP verified successfully")
}

func HandleResendOtp(ctx echo.Context) error {
	return HandleSendOtp(ctx)
}

func HandleForgotPassword(ctx echo.Context) error {
	var req forgotPasswordRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	phone := req.Phone
	userType := req.UserType

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

	issuePhoneOTP(user.ID, user.Phone)

	return responses.SendOK(ctx, nil, "OTP sent successfully")
}

func HandleResetPassword(ctx echo.Context) error {
	phone := ctx.Param("phone")
	var req resetPasswordRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	otp := req.Otp
	password := req.Password
	confirmPassword := req.ConfirmPassword
	userType := req.UserType

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

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to hash password")
	}

	if err := UpdateUser(user.ID, bson.M{"password": hashed, "otp": "", "phone_verified": true}); err != nil {
		return responses.SendError(ctx, 500, "failed to reset password")
	}

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
