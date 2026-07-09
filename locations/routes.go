package locations

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Group) {
	e.GET("/locations", HandleGetLocations)
}

func HandleGetLocations(ctx echo.Context) error {
	locations, err := GetAllLocations()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch locations")
	}
	return responses.SendOK(ctx, locations, "Locations retrieved successfully")
}
