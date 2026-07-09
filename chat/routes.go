package chat

import "github.com/labstack/echo/v4"

func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/job-requests/:jobRequestId/chats", HandleListChats)
	g.POST("/job-requests/:jobRequestId/chats", HandleCreateChat)
}
