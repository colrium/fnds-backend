package users

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	"backend/contextutil"
	"backend/notifications"
	"backend/responses"
	"backend/turnstile"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/crypto/bcrypt"
)

const (
	// OTPTTL is how long a verification code stays valid after it is issued.
	OTPTTL         = 5 * time.Minute
	otpMaxAttempts = 5
)

type verifyOtpRequest struct {
	Otp string `json:"otp"`
}
type forgotPasswordRequest struct {
	Phone          string `json:"phone"`
	UserType       string `json:"user_type"`
	TurnstileToken string `json:"turnstile_token"`
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
	if err := UpdateUser(userID, bson.M{
		"otp":            otp,
		"otp_expires_at": time.Now().UTC().Add(OTPTTL),
		"otp_attempts":   0,
	}); err != nil {
		log.Printf("[otp] failed to persist code for user %s: %v", userID, err)
		return
	}
	if err := notifications.SendOTPSMS(phone, otp); err != nil {
		log.Printf("[otp] failed to send SMS to %s: %v", phone, err)
	}
}

// verifyUserOtp checks the supplied otp against the user's stored code,
// enforcing expiry and a max-attempts lockout. Returns nil when valid.
func verifyUserOtp(user *User, otp string) error {
	if user.Otp == "" {
		return fmt.Errorf("no verification code found; request a new one")
	}
	// Fail closed: a missing expiry (stale pre-feature OTP) is treated as expired,
	// never as "never expires".
	if user.OtpExpiresAt.IsZero() || time.Now().UTC().After(user.OtpExpiresAt) {
		_ = UpdateUser(user.ID, bson.M{"otp": "", "otp_attempts": 0})
		return fmt.Errorf("verification code has expired; request a new one")
	}
	if user.OtpAttempts >= otpMaxAttempts {
		_ = UpdateUser(user.ID, bson.M{"otp": "", "otp_attempts": 0})
		return fmt.Errorf("too many attempts; request a new code")
	}
	if user.Otp != otp {
		_ = IncrementOtpAttempts(user.ID)
		return fmt.Errorf("Invalid OTP")
	}
	return nil
}

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/otp/send", HandleSendOtp)
	g.POST("/otp/verify", HandleVerifyOtp)
	g.POST("/otp/resend", HandleResendOtp)
	g.PATCH("/fcm-token", HandleUpdateToken)
}

// RegisterArtisanRoutes registers artisan-only authenticated routes
func RegisterArtisanRoutes(g *echo.Group) {
	g.POST("/artisan/availability", HandleSetAvailability)
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

	if err := verifyUserOtp(user, otp); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	err = UpdateUser(userID, bson.M{
		"otp":            "",
		"otp_attempts":   0,
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
	if err := turnstile.Verify(
		ctx.Request().Context(),
		req.TurnstileToken,
		ctx.RealIP(),
		"forgot_password",
	); err != nil {
		if errors.Is(err, turnstile.ErrUnavailable) {
			slog.Error("turnstile verification unavailable", "error", err)
			return responses.SendError(
				ctx,
				http.StatusServiceUnavailable,
				"verification service is temporarily unavailable",
			)
		}
		slog.Warn("turnstile verification rejected", "error", err)
		return responses.SendError(ctx, http.StatusBadRequest, "captcha verification failed")
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
	if err == nil {
		issuePhoneOTP(user.ID, user.Phone)
	}
	// Always the same response — do not leak whether the account exists.
	return responses.SendOK(ctx, nil, "If an account with that phone exists, a reset code has been sent.")
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
		return responses.SendError(ctx, 400, "Invalid phone or code")
	}

	if err := verifyUserOtp(user, otp); err != nil {
		return responses.SendError(ctx, 400, "Invalid phone or code")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to hash password")
	}

	if err := UpdateUser(user.ID, bson.M{"password": hashed, "otp": "", "otp_attempts": 0, "phone_verified": true}); err != nil {
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
