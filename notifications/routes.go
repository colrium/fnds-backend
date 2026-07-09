package notifications

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	e.POST("/notify/artisan", HandleSendNotification)
}

func HandleSendNotification(ctx echo.Context) error {
	return responses.SendOK(ctx, nil, "Notification sent successfully")
}
