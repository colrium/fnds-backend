package banks

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	e.GET("/banks", HandleGetBanks)
}

func HandleGetBanks(ctx echo.Context) error {
	banks, err := GetAllBanks()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch banks")
	}
	return responses.SendOK(ctx, banks, "Banks retrieved successfully")
}
