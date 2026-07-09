package reviews

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/reviews", HandleCreateReview)
	g.GET("/reviews", HandleListReviews)
}

func RegisterPublicRoutes(g *echo.Group) {
	g.GET("/reviews/artisan/:artisanId", HandleListReviewsByArtisan)
}
