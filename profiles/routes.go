package profiles

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/profile", HandleGetProfile)
	g.POST("/profile", HandleUpdateProfile)
}

func RegisterPublicRoutes(g *echo.Group) {
	g.GET("/profile/:userId", HandleGetProfileByUserID)
}
