package files

import (
	"time"

	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

func RegisterArtisanRoutes(g *echo.Group, handler *Handler) {
	g.GET("/files", handler.List)
	g.POST(
		"/files/presign",
		handler.Presign,
		echomiddleware.RateLimiterWithConfig(echomiddleware.RateLimiterConfig{
			Store: echomiddleware.NewRateLimiterMemoryStoreWithConfig(
				echomiddleware.RateLimiterMemoryStoreConfig{
					Rate:      rate.Limit(2),
					Burst:     20,
					ExpiresIn: 10 * time.Minute,
				},
			),
		}),
	)
	g.POST("/files/complete", handler.Complete)
	g.DELETE("/files/:id", handler.Delete)
}
