package applications

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleStartup(ctx echo.Context) error {
	token := ctx.FormValue("token")
	if token == "" {
		return responses.SendError(ctx, 403, "Unauthorized")
	}
	return responses.SendOK(ctx, nil, "App is live again.")
}
