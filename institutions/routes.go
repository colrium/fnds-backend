package institutions

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group) {
	g.GET("/institution/all", HandleGetAll)
	g.GET("/institution/:institutionId", HandleGetOne)
	g.POST("/institution/create", HandleCreate)
	g.PATCH("/institution/:institutionId", HandleUpdate)
}
