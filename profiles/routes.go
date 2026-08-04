package profiles

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/profile", HandleGetProfile)
	g.POST("/profile", HandleUpdateProfile)
	g.POST("/profile/public", HandleTogglePublicProfile)
}

func RegisterPublicRoutes(g *echo.Group) {
	// The slug is the canonical public identifier for artisan profiles.
	// If a fundis_id is passed instead, the handler detects the format mismatch
	// and 302-redirects to the slug-based URL.
	g.GET("/artisans/:slug", HandleGetPublicArtisanProfile)
}
