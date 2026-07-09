package users

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleUpdateProfessionalBackground(ctx echo.Context) error {
	return responses.SendOK(ctx, map[string]interface{}{"saved": true}, "Record created successfully!")
}

func HandleUpdateLegalDocument(ctx echo.Context) error {
	return responses.SendOK(ctx, map[string]interface{}{"saved": true}, "File saved successfully!")
}

func HandleUpdateAvatar(ctx echo.Context) error {
	return responses.SendOK(ctx, map[string]interface{}{"avatar": true}, "Avatar Updated Successfully!")
}
