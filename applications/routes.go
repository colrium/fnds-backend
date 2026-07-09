package applications

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group) {
	g.POST("/startup", HandleStartup)
}
