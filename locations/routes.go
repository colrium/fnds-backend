package locations

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group) {
	g.GET("/locations", HandleGetLocations)
}
