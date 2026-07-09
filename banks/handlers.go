package banks

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleGetBanks(ctx echo.Context) error {
	bankList, err := GetAllBanks()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch banks")
	}
	return responses.SendOK(ctx, bankList, "Banks retrieved successfully")
}
