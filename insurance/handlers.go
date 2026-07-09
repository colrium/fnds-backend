package insurance

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleCreateInsurance(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req struct {
		Type           string  `json:"type"`
		Option         string  `json:"option"`
		TotalToolValue float64 `json:"total_tool_value"`
		FamilySize     int     `json:"family_size"`
		NextOfKin      string  `json:"next_of_kin"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateCreateInsurance(req.Type, req.Option); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	switch req.Type {
	case "business tools insurance":
		if err := ValidateBusinessToolsInsurance(req.TotalToolValue); err != nil {
			return responses.SendError(ctx, 400, err.Error())
		}
	case "health insurance":
		if err := ValidateHealthInsurance(req.FamilySize); err != nil {
			return responses.SendError(ctx, 400, err.Error())
		}
	case "accident and last respect insurance":
		if err := ValidateAccidentInsurance(req.NextOfKin); err != nil {
			return responses.SendError(ctx, 400, err.Error())
		}
	}

	insurance := Insurance{
		ArtisanID:      userID,
		Type:           req.Type,
		Option:         req.Option,
		TotalToolValue: req.TotalToolValue,
		FamilySize:     req.FamilySize,
		NextOfKin:      req.NextOfKin,
	}

	if err := CreateInsurance(&insurance); err != nil {
		return responses.SendError(ctx, 500, "Failed to create insurance")
	}

	return responses.SendCreated(ctx, insurance, "Insurance created successfully")
}

func HandleListInsurances(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	insurances, err := ListInsurancesByArtisan(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch insurances")
	}
	return responses.SendOK(ctx, insurances, "Insurances retrieved successfully")
}

func HandleGetInsurance(ctx echo.Context) error {
	id := ctx.Param("id")
	insurance, err := GetInsuranceByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Insurance not found")
	}
	return responses.SendOK(ctx, insurance, "Insurance retrieved successfully")
}
