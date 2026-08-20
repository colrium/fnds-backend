package profileapproval

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"backend/contextutil"
	"backend/notifications"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
)

func HandleSubmitProfileForReview(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	if err := SubmitForReview(ctx.Request().Context(), userID); err != nil {
		if errors.Is(err, ErrProfileIncomplete) {
			return responses.SendError(ctx, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, ErrAlreadyPending) {
			return responses.SendError(ctx, http.StatusConflict, err.Error())
		}
		return responses.SendError(ctx, http.StatusInternalServerError, "failed to submit profile for review")
	}

	// Send Slack notification asynchronously (best-effort).
	go func(userID string) {
		bgCtx := context.Background()
		user, err := users.GetUserByID(bgCtx, userID)
		var userName, userEmail string
		if err == nil {
			userName = user.FirstName + " " + user.LastName
			userEmail = user.Email
		} else {
			slog.Warn("profile submitted: failed to load user for notification", "user_id", userID, "error", err)
		}
		if notifErr := notifications.NotifyProfileSubmitted(bgCtx, notifications.ProfileSubmittedActivity{
			UserID:      userID,
			UserName:    userName,
			UserEmail:   userEmail,
			SubmittedAt: time.Now().UTC(),
		}); notifErr != nil {
			slog.Error("profile submitted: slack notification failed", "user_id", userID, "error", notifErr)
		}
	}(userID)

	return responses.SendOK(ctx, nil, "Profile submitted for review")
}
