package insurance

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/insurances", HandleCreateInsurance)
	g.GET("/insurances", HandleListInsurances)
	g.GET("/insurances/:id", HandleGetInsurance)
}
