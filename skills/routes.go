package skills

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	e.GET("/request-types", HandleGetSkills)
}

func HandleGetSkills(ctx echo.Context) error {
	skills, err := GetAllSkills()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch skills")
	}
	return responses.SendOK(ctx, skills, "Skills retrieved successfully")
}
