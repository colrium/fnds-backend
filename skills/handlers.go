package skills

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleGetSkills(ctx echo.Context) error {
	skillList, err := GetAllSkills()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch skills")
	}
	return responses.SendOK(ctx, skillList, "Skills retrieved successfully")
}
