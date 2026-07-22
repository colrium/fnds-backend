package referrals

import (
	"errors"
	"net/http"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/referrals/code", HandleGetCode)
	g.GET("/referrals", HandleGetStats)
}

func HandleGetCode(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	userType, err := AccountType(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	code, err := GetOrCreateCode(ctx.Request().Context(), userID, userType)
	if errors.Is(err, ErrNotWorker) {
		return responses.SendError(ctx, http.StatusForbidden, err.Error())
	}
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to create referral code")
	}
	return responses.SendOK(ctx, code, "Referral code retrieved successfully")
}

func HandleGetStats(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	userType, err := AccountType(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	if userType != "artisan" {
		return responses.SendError(ctx, http.StatusForbidden, ErrNotWorker.Error())
	}
	stats, err := Stats(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to fetch referrals")
	}
	return responses.SendOK(ctx, stats, "Referrals retrieved successfully")
}
