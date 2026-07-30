package institutions

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"backend/responses"

	"github.com/labstack/echo/v4"
)

// MinSearchQueryLength is the minimum number of runes required in a search
// query before we hit the database. Shorter queries are rejected to avoid
// full-collection scans on near-empty input.
const MinSearchQueryLength = 2

// DefaultPageLimit and MaxPageLimit bound the number of institutions returned
// per request.
const (
	DefaultPageLimit int64 = 20
	MaxPageLimit     int64 = 100
)

func HandleGetAll(ctx echo.Context) error {
	q := ctx.QueryParam("q")

	if q != "" && utf8.RuneCountInString(q) < MinSearchQueryLength {
		return responses.SendError(ctx, 400, "Search query must be at least 2 characters")
	}

	limit, err := parsePositiveInt(ctx.QueryParam("limit"), DefaultPageLimit)
	if err != nil {
		return responses.SendError(ctx, 400, fmt.Sprintf("Invalid limit: %s", err))
	}
	limit = min(limit, MaxPageLimit)

	offset, err := parsePositiveInt(ctx.QueryParam("offset"), 0)
	if err != nil {
		return responses.SendError(ctx, 400, fmt.Sprintf("Invalid offset: %s", err))
	}

	institutions, err := SearchInstitutions(q, limit, offset)
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
	var updates map[string]any
	if err := ctx.Bind(&updates); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := UpdateInstitution(id, updates); err != nil {
		return responses.SendError(ctx, 500, "Failed to update institution")
	}
	return responses.SendOK(ctx, nil, "Institution updated successfully")
}

func parsePositiveInt(raw string, fallback int64) (int64, error) {
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid integer", raw)
	}
	if v < 0 {
		return 0, fmt.Errorf("%d must be >= 0", v)
	}
	return v, nil
}
