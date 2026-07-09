package quotations

import (
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/quotations", HandleCreateQuotation)
	g.GET("/quotations", HandleListQuotations)
	g.GET("/quotations/:id", HandleGetQuotation)
	g.PATCH("/quotations/:id", HandleUpdateQuotation)
}

func HandleCreateQuotation(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var q Quotation
	if err := ctx.Bind(&q); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	q.ArtisanID = userID
	q.Status = "pending"

	if err := CreateQuotation(&q); err != nil {
		return responses.SendError(ctx, 500, "Failed to create quotation")
	}
	return responses.SendCreated(ctx, q, "Quotation created successfully")
}

func HandleListQuotations(ctx echo.Context) error {
	quotations, err := ListQuotations(map[string]interface{}{})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch quotations")
	}
	return responses.SendOK(ctx, quotations, "Quotations retrieved successfully")
}

func HandleGetQuotation(ctx echo.Context) error {
	id := ctx.Param("id")
	q, err := GetQuotationByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Quotation not found")
	}
	return responses.SendOK(ctx, q, "Quotation retrieved successfully")
}

func HandleUpdateQuotation(ctx echo.Context) error {
	id := ctx.Param("id")
	var updates map[string]interface{}
	if err := ctx.Bind(&updates); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := UpdateQuotation(id, updates); err != nil {
		return responses.SendError(ctx, 500, "Failed to update quotation")
	}
	return responses.SendOK(ctx, nil, "Quotation updated successfully")
}
