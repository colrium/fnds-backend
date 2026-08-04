package notifications

import "github.com/labstack/echo/v4"

// RegisterAuthRoutes binds the notifications endpoints to handler. Callers
// should share one Handler (backed by one CachedStore) across the process so
// in-process cache coalescing is effective; see ConfigureDefaultStore.
func RegisterAuthRoutes(group *echo.Group, handler *Handler) {
	group.GET("/notifications", handler.HandleList)
	group.PATCH("/notifications/read-all", handler.HandleMarkAllRead)
	group.PATCH("/notifications/:id/read", handler.HandleMarkRead)
}
