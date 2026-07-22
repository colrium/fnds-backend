package notifications

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(group *echo.Group) {
	group.GET("/notifications", HandleList)
	group.PATCH("/notifications/read-all", HandleMarkAllRead)
	group.PATCH("/notifications/:id/read", HandleMarkRead)
}
