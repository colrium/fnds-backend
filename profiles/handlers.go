package profiles

import (
	"errors"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleGetProfile(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	profile, err := GetFullProfile(userID)
	if err != nil {
		return responses.SendError(ctx, 404, "Profile not found")
	}

	return responses.SendOK(ctx, profile, "Profile retrieved successfully")
}

func HandleUpdateProfile(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req ProfileUpdateRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := UpdateProfile(userID, req); err != nil {
		if errors.Is(err, ErrInvalidSkillSelection) {
			return responses.SendError(ctx, 400, err.Error())
		}
		return responses.SendError(ctx, 500, err.Error())
	}

	return responses.SendOK(ctx, nil, "Profile updated successfully")
}

func HandleGetProfileByUserID(ctx echo.Context) error {
	userID := ctx.Param("userId")
	if userID == "" {
		return responses.SendError(ctx, 400, "User ID is required")
	}

	profile, err := GetFullProfile(userID)
	if err != nil {
		return responses.SendError(ctx, 404, "Profile not found")
	}

	return responses.SendOK(ctx, profile, "Profile retrieved successfully")
}
