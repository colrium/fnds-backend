package documenttypes

import (
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleListDocumentTypes(ctx echo.Context) error {
	types, err := ListDocumentTypes()
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch document types")
	}
	return responses.SendOK(ctx, types, "Document types retrieved successfully")
}

func HandleCreateDocumentType(ctx echo.Context) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateCreateDocumentType(req.Name); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	dt := DocumentType{Name: req.Name}
	if err := CreateDocumentType(&dt); err != nil {
		return responses.SendError(ctx, 500, "Failed to create document type")
	}
	return responses.SendCreated(ctx, dt, "Document type created successfully")
}

func HandleUpdateDocumentType(ctx echo.Context) error {
	id := ctx.Param("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateUpdateDocumentType(req.Name); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	if err := UpdateDocumentType(id, map[string]interface{}{"name": req.Name}); err != nil {
		return responses.SendError(ctx, 500, "Failed to update document type")
	}
	return responses.SendOK(ctx, nil, "Document type updated successfully")
}

func HandleDeleteDocumentType(ctx echo.Context) error {
	id := ctx.Param("id")
	if err := DeleteDocumentType(id); err != nil {
		return responses.SendError(ctx, 500, "Failed to delete document type")
	}
	return responses.SendOK(ctx, nil, "Document type deleted successfully")
}
