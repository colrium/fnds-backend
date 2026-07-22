package files

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(g *echo.Group, handler *Handler) {
	g.GET("/files", handler.List)
	g.POST("/files/presign", handler.Presign)
	g.POST("/files/complete", handler.Complete)
	g.DELETE("/files/:id", handler.Delete)
}
