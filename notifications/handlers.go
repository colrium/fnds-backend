package notifications

import (
	"net/http"
	"strconv"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleList(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	limit, err := strconv.ParseInt(ctx.QueryParam("limit"), 10, 64)
	if err != nil || limit < 1 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}

	feed, err := NewMongoStore().List(ctx.Request().Context(), userID, limit)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load notifications")
	}
	return responses.SendOK(ctx, feed, "Notifications retrieved successfully")
}

func HandleMarkRead(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	notificationID := ctx.Param("id")
	if notificationID == "" {
		return responses.SendError(ctx, http.StatusBadRequest, "Notification ID is required")
	}

	updated, err := NewMongoStore().MarkRead(
		ctx.Request().Context(),
		userID,
		notificationID,
	)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to update notification")
	}
	if !updated {
		return responses.SendError(ctx, http.StatusNotFound, "Notification not found")
	}
	return responses.SendOK(ctx, nil, "Notification marked as read")
}

func HandleMarkAllRead(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	if err := NewMongoStore().MarkAllRead(ctx.Request().Context(), userID); err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to update notifications")
	}
	return responses.SendOK(ctx, nil, "Notifications marked as read")
}
