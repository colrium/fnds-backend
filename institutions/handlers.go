package institutions

import (
	"unicode/utf8"

	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleGetAll(ctx echo.Context) error {
	q := ctx.QueryParam("q")

	// Queries shorter than 2 characters are too broad, return an empty list.
	if q != "" && utf8.RuneCountInString(q) < 2 {
		return responses.SendOK(ctx, []Institution{}, "Institutions retrieved successfully")
	}

	institutions, err := SearchInstitutions(q)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch institutions")
	}
	return responses.SendOK(ctx, institutions, "Institutions retrieved successfully")
}

func HandleGetOne(ctx echo.Context) error {
	id := ctx.Param("institutionId")
	inst, err := GetInstitutionByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Institution not found")
	}
	return responses.SendOK(ctx, inst, "Institution retrieved successfully")
}

func HandleCreate(ctx echo.Context) error {
	var inst Institution
	if err := ctx.Bind(&inst); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := CreateInstitution(&inst); err != nil {
		return responses.SendError(ctx, 500, "Failed to create institution")
	}
	return responses.SendCreated(ctx, inst, "Institution created successfully")
}

func HandleUpdate(ctx echo.Context) error {
	id := ctx.Param("institutionId")
	var updates map[string]interface{}
	if err := ctx.Bind(&updates); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := UpdateInstitution(id, updates); err != nil {
		return responses.SendError(ctx, 500, "Failed to update institution")
	}
	return responses.SendOK(ctx, nil, "Institution updated successfully")
}
