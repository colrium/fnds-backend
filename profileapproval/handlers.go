package profileapproval

import (
	"errors"
	"net/http"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleSubmitProfileForReview(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	if err := SubmitForReview(ctx.Request().Context(), userID); err != nil {
		if errors.Is(err, ErrProfileIncomplete) {
			return responses.SendError(ctx, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, ErrAlreadyPending) {
			return responses.SendError(ctx, http.StatusConflict, err.Error())
		}
		return responses.SendError(ctx, http.StatusInternalServerError, "failed to submit profile for review")
	}

	return responses.SendOK(ctx, nil, "Profile submitted for review")
}
