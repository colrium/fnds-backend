package profileapproval

import "github.com/labstack/echo/v4"

// RegisterAuthRoutes registers the artisan-facing profile-review routes on
// the existing JWT-authenticated group (wired in main.go alongside
// profiles.RegisterAuthRoutes and the other per-domain auth route
// registrations).
func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/profile/submit-review", HandleSubmitProfileForReview)
}
