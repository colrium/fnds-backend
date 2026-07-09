package notifications

import "github.com/labstack/echo/v4"

func RegisterPublicRoutes(g *echo.Group) {
	g.POST("/notify/artisan", HandleSendNotification)
}
