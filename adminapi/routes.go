package adminapi

import "github.com/labstack/echo/v4"

// RegisterAdminRoutes registers the admin data-management endpoints on the
// existing /api/v1/admin group (JWTAuth + IsAdmin, wired in main.go).
func RegisterAdminRoutes(g *echo.Group) {
	g.GET("/users", HandleListUsers)
	g.GET("/users/:id", HandleGetUser)
	g.PATCH("/users/:id/status", HandleUpdateUserStatus)
	g.PATCH("/users/:id/user-type", HandleUpdateUserType)
	g.POST("/users/:id/resend-otp", HandleResendUserOtp)
	g.PATCH("/users/:id/contact", HandleUpdateUserContact)
	g.GET("/stats", HandleGetStats)
	g.GET("/profile-reviews", HandleListProfileReviews)
	g.GET("/profile-reviews/:user_id", HandleGetProfileReview)
	g.POST("/profile-reviews/:user_id/approve", HandleApproveProfileReview)
	g.POST("/profile-reviews/:user_id/reject", HandleRejectProfileReview)
}
