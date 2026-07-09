package documenttypes

import "github.com/labstack/echo/v4"

func RegisterRoutes(g *echo.Group) {
	g.GET("/document-types", HandleListDocumentTypes)
}

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/document-types", HandleCreateDocumentType)
	g.PATCH("/document-types/:id", HandleUpdateDocumentType)
	g.DELETE("/document-types/:id", HandleDeleteDocumentType)
}
