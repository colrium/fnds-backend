package notifications

import (
	"net/http"
	"strconv"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

const (
	defaultListLimit = 30
	maxListLimit     = 100
)

// Handler exposes the notifications HTTP endpoints against a store that
// implements the reader/mutator behavior. Cache behavior, if any, lives
// entirely behind that store; the handler is unaware of Redis.
type Handler struct {
	store interface {
		feedReader
		feedMutator
	}
}

// NewHandler constructs a Handler backed by store. Tests can pass a bare
// MongoStore, a fake, or the shared CachedStore configured at startup.
func NewHandler(store interface {
	feedReader
	feedMutator
}) *Handler {
	return &Handler{store: store}
}

func normalizeListLimit(raw string) int64 {
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit < 1 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func (h *Handler) HandleList(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	limit := normalizeListLimit(ctx.QueryParam("limit"))

	feed, err := h.store.List(ctx.Request().Context(), userID, limit)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load notifications")
	}
	return responses.SendOK(ctx, feed, "Notifications retrieved successfully")
}

func (h *Handler) HandleMarkRead(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	notificationID := ctx.Param("id")
	if notificationID == "" {
		return responses.SendError(ctx, http.StatusBadRequest, "Notification ID is required")
	}

	updated, err := h.store.MarkRead(
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

func (h *Handler) HandleMarkAllRead(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	if err := h.store.MarkAllRead(ctx.Request().Context(), userID); err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to update notifications")
	}
	return responses.SendOK(ctx, nil, "Notifications marked as read")
}
