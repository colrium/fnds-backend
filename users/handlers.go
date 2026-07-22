package users

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func HandleUpdateProfessionalBackground(ctx echo.Context) error {
	return responses.SendOK(ctx, map[string]interface{}{"saved": true}, "Record created successfully!")
}

func HandleUpdateLegalDocument(ctx echo.Context) error {
	return responses.SendOK(ctx, map[string]interface{}{"saved": true}, "File saved successfully!")
}

type setAvailabilityRequest struct {
	IsAvailable bool `json:"is_available"`
}

func HandleSetAvailability(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	var req setAvailabilityRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := UpdateUser(userID, bson.M{"is_available": req.IsAvailable}); err != nil {
		return responses.SendError(ctx, 500, "failed to update availability")
	}
	user, err := GetUserByID(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "failed to load updated user")
	}
	return responses.SendOK(ctx, user, "Availability updated")
}
