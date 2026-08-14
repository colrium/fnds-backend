package adminapi

import "github.com/labstack/echo/v4"

// RegisterAdminRoutes registers the admin data-management endpoints on the
// existing /api/v1/admin group (JWTAuth + IsAdmin, wired in main.go).
func RegisterAdminRoutes(g *echo.Group) {
	g.GET("/users", HandleListUsers)
	g.GET("/users/:id", HandleGetUser)
	g.PATCH("/users/:id/status", HandleUpdateUserStatus)
	g.GET("/stats", HandleGetStats)
}
