package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"backend/notifications"
	"backend/referrals"
	"backend/responses"
	"backend/turnstile"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterPublicRoutes(g *echo.Group) {
	g.POST("/register", HandleUserRegistration)
	g.POST("/login", HandleUserLogin)
	g.POST("/logout", HandleLogout)
	g.POST("/refresh", HandleRefresh)
	g.POST("/me", HandleMe)
	g.GET("/login/:provider", HandleSocialLogin)
	g.POST("/login/:provider/callback", HandleSocialLogin)
}

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/admin/users", HandleCreateUserViaAdmin)
}

type RegisterRequest struct {
	FirstName      string   `json:"first_name" form:"first_name"`
	LastName       string   `json:"last_name" form:"last_name"`
	Email          string   `json:"email" form:"email"`
	Phone          string   `json:"phone" form:"phone"`
	Password       string   `json:"password" form:"password"`
	UserType       string   `json:"user_type" form:"user_type"`
	Skills         []string `json:"skills" form:"skills"`
	TurnstileToken string   `json:"turnstile_token" form:"turnstile_token"`
	ReferralCode   string   `json:"referral_code" form:"referral_code"`
}

type LoginRequest struct {
	Username       string `json:"username" form:"username"`
	Password       string `json:"password" form:"password"`
	TurnstileToken string `json:"turnstile_token" form:"turnstile_token"`
}

type SocialLoginRequest struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	AccessToken string `json:"access_token"`
	Phone       string `json:"phone"`
	UserType    string `json:"user_type"`
	IDNumber    string `json:"id_number"`
	Email       string `json:"email"`
	Avatar      string `json:"avatar"`
	ProviderID  string `json:"provider_id"`
}

func HandleUserLogin(ctx echo.Context) error {
	var req LoginRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := turnstile.Verify(ctx.Request().Context(), req.TurnstileToken, ctx.RealIP(), "login"); err != nil {
		return sendTurnstileError(ctx, err)
	}

	if req.Username == "" || req.Password == "" {
		return responses.SendError(ctx, 400, "username and password are required")
	}

	username := strings.TrimSpace(req.Username)
	var email, phone string

	if _, err := mail.ParseAddress(username); err == nil {
		email = username
	} else {
		phone = username
	}

	user, token, err := processUserLogin(email, phone, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, ErrPasswordResetRequired):
			return responses.SendOK(ctx, map[string]interface{}{
				"reset_required": true,
				"user_type":      user.UserType,
			}, "This account was migrated from the old platform. Please reset your password to continue.")
		case errors.Is(err, ErrAccountPending):
			return responses.SendError(ctx, 403, "Your account is pending phone verification. Please verify your phone number to continue.")
		case errors.Is(err, ErrAccountSuspended):
			return responses.SendError(ctx, 403, "Your account has been suspended.")
		default:
			return responses.SendError(ctx, 401, "invalid credentials")
		}
	}

	return responses.SendOK(ctx, map[string]interface{}{
		"token": map[string]interface{}{
			"access_token": token,
			"token_type":   "bearer",
			"expires_in":   86400,
		},
		"user": user,
	}, "User login successfully.")
}

func HandleUserRegistration(ctx echo.Context) error {
	var req RegisterRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := turnstile.Verify(ctx.Request().Context(), req.TurnstileToken, ctx.RealIP(), "register"); err != nil {
		return sendTurnstileError(ctx, err)
	}

	if req.FirstName == "" || req.LastName == "" || req.Phone == "" || req.Password == "" {
		return responses.SendError(ctx, 400, "first_name, last_name, phone, and password are required")
	}

	if req.UserType == "" {
		return responses.SendError(ctx, 400, "user_type is required")
	}

	_validTypes := map[string]bool{"customer": true, "artisan": true}
	if !_validTypes[req.UserType] {
		return responses.SendError(ctx, 400, "invalid user_type")
	}

	var (
		referralCode *referrals.Code
		err          error
	)
	if strings.TrimSpace(req.ReferralCode) != "" {
		if req.UserType != "artisan" {
			return responses.SendError(ctx, 400, "referral codes are only valid for worker registrations")
		}
		referralCode, err = referrals.ResolveCode(ctx.Request().Context(), req.ReferralCode)
		if err != nil {
			return responses.SendError(ctx, 400, "invalid referral code")
		}
	}

	if len(req.Password) < 8 {
		return responses.SendError(ctx, 400, "password must be at least 8 characters")
	}

	user, err := createNewUser(
		req.FirstName,
		req.LastName,
		req.Email,
		req.Phone,
		req.Password,
		req.UserType,
		referrals.NormalizeCode(req.ReferralCode),
	)
	if err != nil {
		if errors.Is(err, ErrDuplicateUser) {
			return responses.SendError(ctx, 409, ErrDuplicateUser.Error())
		}
		return responses.SendError(ctx, 500, "failed to create user: "+err.Error())
	}

	// Fire-and-forget Slack notification for new registration.
	go func(u users.User) {
		registeredAt := u.CreatedAt
		if registeredAt.IsZero() {
			registeredAt = time.Now().UTC()
		}
		_ = notifications.NotifyUserRegistered(context.Background(), notifications.RegistrationActivity{
			UserName:     strings.TrimSpace(u.FirstName + " " + u.LastName),
			UserEmail:    u.Email,
			UserPhone:    u.Phone,
			UserID:       u.ID,
			RegisteredAt: registeredAt,
		})
	}(*user)

	if referralCode != nil {
		if err := referrals.CreatePending(ctx.Request().Context(), referralCode, user.ID); err != nil {
			slog.Error("failed to record referral", "referred_user_id", user.ID, "error", err)
		}
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to generate token")
	}

	return responses.SendOK(ctx, map[string]interface{}{
		"token": map[string]interface{}{
			"access_token": token,
			"token_type":   "bearer",
			"expires_in":   86400,
		},
		"user": user,
	}, "User registered successfully.")
}

func sendTurnstileError(ctx echo.Context, err error) error {
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

func HandleLogout(ctx echo.Context) error {
	return responses.SendOK(ctx, nil, "User logged out successfully.")
}

func HandleRefresh(ctx echo.Context) error {
	user := ctx.Get("user")
	if user == nil {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	return responses.SendOK(ctx, nil, "User refreshed successfully.")
}

func HandleMe(ctx echo.Context) error {
	user := ctx.Get("user")
	if user == nil {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	return responses.SendOK(ctx, user, "User retrieved successfully.")
}

func HandleSocialLogin(ctx echo.Context) error {
	var req SocialLoginRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	provider := ctx.Param("provider")
	if provider != "facebook" && provider != "google" {
		return responses.SendError(ctx, 422, "Please login using facebook or google")
	}

	if req.Email == "" || req.AccessToken == "" || req.ProviderID == "" {
		return responses.SendError(ctx, 400, "email, access_token, and provider_id are required")
	}

	if req.UserType == "" {
		req.UserType = "customer"
	}

	user, err := users.GetUserByEmailAndType(req.Email, req.UserType)
	if err != nil {
		user, err = createNewUser(req.FirstName, req.LastName, req.Email, req.Phone, "social_login_placeholder", req.UserType, "")
		if err != nil {
			if errors.Is(err, ErrDuplicateUser) {
				return responses.SendError(ctx, 409, ErrDuplicateUser.Error())
			}
			return responses.SendError(ctx, 500, "failed to create user")
		}
		if err := users.UpdateUser(user.ID, bson.M{"status": users.StatusActive, "is_active": true}); err != nil {
			return responses.SendError(ctx, 500, "failed to activate user")
		}
		user.Status = users.StatusActive
		user.IsActive = true
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to generate token")
	}

	return responses.SendOK(ctx, map[string]interface{}{
		"token": map[string]interface{}{
			"access_token": token,
			"token_type":   "bearer",
			"expires_in":   86400,
		},
		"user": user,
	}, "access granted!")
}

func HandleShareUserRecord(ctx echo.Context) error {
	fundisID := ctx.Param("fundis_id")
	user, err := users.GetArtisanSummaryByFundisID(fundisID)
	if err != nil {
		return responses.SendError(ctx, 404, "Invalid Id! Kindly ensure that user is an Artisan")
	}
	return responses.SendOK(ctx, user, "Data Retrieved Successfully")
}

func HandleAllArtisans(ctx echo.Context) error {
	page := int64(1)
	limit := int64(50)

	artisans, total, err := users.ListArtisanSummaries(page, limit)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to fetch artisans")
	}

	return responses.SendOK(ctx, map[string]interface{}{
		"data":  artisans,
		"total": total,
	}, "Artisans Retrieved Successfully")
}

func HandleCreateUserViaAdmin(ctx echo.Context) error {
	var req RegisterRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if req.FirstName == "" || req.LastName == "" || req.Phone == "" || req.Password == "" {
		return responses.SendError(ctx, 400, "first_name, last_name, phone, and password are required")
	}

	if req.UserType == "" {
		return responses.SendError(ctx, 400, "user_type is required")
	}

	_validTypes := map[string]bool{"customer": true, "artisan": true, "assessor": true}
	if !_validTypes[req.UserType] {
		return responses.SendError(ctx, 400, "invalid user_type")
	}

	user, err := createUserViaAdmin(req.FirstName, req.LastName, req.Email, req.Phone, req.Password, req.UserType)
	if err != nil {
		if errors.Is(err, ErrDuplicateUser) {
			return responses.SendError(ctx, 409, ErrDuplicateUser.Error())
		}
		return responses.SendError(ctx, 500, "failed to create user: "+err.Error())
	}

	return responses.SendOK(ctx, user, "User created successfully.")
}
