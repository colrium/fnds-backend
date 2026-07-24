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
	"backend/referrals"
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
	Email          string `json:"email"`
	UserType       string `json:"user_type"`
	TurnstileToken string `json:"turnstile_token"`
}
type resetPasswordRequest struct {
	Phone           string `json:"phone"`
	Email           string `json:"email"`
	Otp             string `json:"otp"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
	UserType        string `json:"user_type"`
}
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
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

// issueEmailOTP generates a fresh code, persists it, and sends it by email.
func issueEmailOTP(userID, email string) {
	otp := GenerateOTP()
	if err := UpdateUser(userID, bson.M{
		"otp":            otp,
		"otp_expires_at": time.Now().UTC().Add(OTPTTL),
		"otp_attempts":   0,
	}); err != nil {
		log.Printf("[otp] failed to persist code for user %s: %v", userID, err)
		return
	}
	if err := notifications.SendPasswordResetOTPEmail(email, otp); err != nil {
		log.Printf("[otp] failed to send email OTP to %s: %v", email, err)
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
	g.POST("/change-password", HandleChangePassword)
}

// RegisterArtisanRoutes registers artisan-only authenticated routes
func RegisterArtisanRoutes(g *echo.Group) {
	g.POST("/artisan/availability", HandleSetAvailability)
}

func RegisterPublicRoutes(g *echo.Group) {
	g.POST("/forgot-password", HandleForgotPassword)
	g.POST("/reset-password", HandleResetPassword)
	// Legacy route kept for backward compatibility with existing clients.
	g.POST("/reset-password/:phone", HandleResetPasswordByPhone)
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
		if err := qualifyReferral(ctx, user); err != nil {
			return responses.SendError(ctx, 500, "failed to qualify referral")
		}
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
	if err := qualifyReferral(ctx, user); err != nil {
		return responses.SendError(ctx, 500, "phone verified but referral qualification failed")
	}

	return responses.SendOK(ctx, nil, "OTP verified successfully")
}

func qualifyReferral(ctx echo.Context, user *User) error {
	if user.ReferralCodeUsed == "" {
		return nil
	}
	if err := referrals.EnsureQualified(
		ctx.Request().Context(),
		user.ReferralCodeUsed,
		user.ID,
	); err != nil {
		slog.Error("failed to qualify referral", "referred_user_id", user.ID, "error", err)
		return err
	}
	return nil
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
	email := req.Email

	if phone == "" && email == "" {
		return responses.SendError(ctx, 400, "phone or email is required")
	}

	// Phone and email are globally unique, so the identifier alone resolves a
	// single account. Look up the user and send OTP via the corresponding
	// channel. Never reveal whether the account exists.
	if phone != "" {
		user, err := GetUserByPhone(phone)
		if err == nil {
			issuePhoneOTP(user.ID, user.Phone)
		}
	} else {
		user, err := GetUserByEmail(email)
		if err == nil {
			issueEmailOTP(user.ID, user.Email)
		}
	}

	return responses.SendOK(ctx, nil, "If an account with that identifier exists, a reset code has been sent.")
}

func HandleResetPassword(ctx echo.Context) error {
	var req resetPasswordRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	phone := req.Phone
	email := req.Email
	otp := req.Otp
	password := req.Password
	confirmPassword := req.ConfirmPassword

	if (phone == "" && email == "") || otp == "" || password == "" || confirmPassword == "" {
		return responses.SendError(ctx, 400, "All fields are required")
	}

	if password != confirmPassword {
		return responses.SendError(ctx, 400, "Passwords do not match")
	}

	var user *User
	var err error
	if phone != "" {
		user, err = GetUserByPhone(phone)
	} else {
		user, err = GetUserByEmail(email)
	}
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid credentials or code")
	}

	if err := verifyUserOtp(user, otp); err != nil {
		return responses.SendError(ctx, 400, "Invalid credentials or code")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to hash password")
	}

	if err := UpdateUser(user.ID, bson.M{"password": hashed, "otp": "", "otp_attempts": 0, "phone_verified": true, "migrated": false}); err != nil {
		return responses.SendError(ctx, 500, "failed to reset password")
	}

	// Notify user via email if they have one on file.
	if user.Email != "" {
		go func() {
			name := user.FirstName
			if name == "" {
				name = "there"
			}
			if err := notifications.SendPasswordResetConfirmationEmail(user.Email, name); err != nil {
				log.Printf("[password-reset] failed to send confirmation email to %s: %v", user.Email, err)
			}
		}()
	}

	return responses.SendOK(ctx, nil, "Password reset successfully")
}

// HandleResetPasswordByPhone is the legacy endpoint kept for backward compatibility.
func HandleResetPasswordByPhone(ctx echo.Context) error {
	phone := ctx.Param("phone")
	var req resetPasswordRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	otp := req.Otp
	password := req.Password
	confirmPassword := req.ConfirmPassword

	if phone == "" || otp == "" || password == "" || confirmPassword == "" {
		return responses.SendError(ctx, 400, "All fields are required")
	}

	if password != confirmPassword {
		return responses.SendError(ctx, 400, "Passwords do not match")
	}

	user, err := GetUserByPhone(phone)
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

	if err := UpdateUser(user.ID, bson.M{"password": hashed, "otp": "", "otp_attempts": 0, "phone_verified": true, "migrated": false}); err != nil {
		return responses.SendError(ctx, 500, "failed to reset password")
	}

	// Notify user via email if they have one on file.
	if user.Email != "" {
		go func() {
			name := user.FirstName
			if name == "" {
				name = "there"
			}
			if err := notifications.SendPasswordResetConfirmationEmail(user.Email, name); err != nil {
				log.Printf("[password-reset] failed to send confirmation email to %s: %v", user.Email, err)
			}
		}()
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

// HandleChangePassword allows an authenticated user to change their password
// by providing their current password and choosing a new one.
func HandleChangePassword(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req changePasswordRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if req.CurrentPassword == "" || req.NewPassword == "" || req.ConfirmPassword == "" {
		return responses.SendError(ctx, 400, "All fields are required")
	}

	if req.NewPassword != req.ConfirmPassword {
		return responses.SendError(ctx, 400, "New passwords do not match")
	}

	if req.CurrentPassword == req.NewPassword {
		return responses.SendError(ctx, 400, "New password must be different from current password")
	}

	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 400, "Invalid user")
	}

	// Verify the current password.
	if err := bcrypt.CompareHashAndPassword(user.Password, []byte(req.CurrentPassword)); err != nil {
		return responses.SendError(ctx, 400, "Current password is incorrect")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 14)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to hash password")
	}

	if err := UpdateUser(user.ID, bson.M{"password": hashed}); err != nil {
		return responses.SendError(ctx, 500, "failed to update password")
	}

	// Notify user via email if they have one on file.
	if user.Email != "" {
		go func() {
			name := user.FirstName
			if name == "" {
				name = "there"
			}
			if err := notifications.SendPasswordChangedEmail(user.Email, name); err != nil {
				log.Printf("[password-change] failed to send notification email to %s: %v", user.Email, err)
			}
		}()
	}

	return responses.SendOK(ctx, nil, "Password changed successfully")
}
