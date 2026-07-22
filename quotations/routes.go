package quotations

import (
	"log/slog"
	"net/http"
	"strings"

	"backend/configs"
	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/quotations", HandleCreateQuotation)
	g.GET("/quotations", HandleListQuotations)
	g.GET("/quotations/:id", HandleGetQuotation)
	g.PATCH("/quotations/:id", HandleUpdateQuotation)

	g.POST("/external-quotations", HandleCreateExternalQuotation)
	g.GET("/external-quotations", HandleListExternalQuotations)
	g.GET("/external-quotations/:id", HandleGetExternalQuotation)
	g.PATCH("/external-quotations/:id/status/:action", HandleUpdateExternalQuotationStatus)

	g.POST("/external-invoices", HandleCreateExternalInvoice)
	g.GET("/external-invoices", HandleListExternalInvoices)
	g.GET("/external-invoices/:id", HandleGetExternalInvoice)
	g.POST("/external-invoices/:id/resend-link", HandleResendInvoicePaymentLink)

	g.POST("/external-payment-bills", HandleCreateExternalPaymentBill)
	g.GET("/external-payment-bills", HandleListExternalPaymentBills)
}

func RegisterPublicRoutes(g *echo.Group) {
	g.GET("/external-quotations/:id/status/:action", HandleUpdateExternalQuotationStatus)
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
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	quotations, err := ListQuotations(relatedQuotationFilter(userID))
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch quotations")
	}
	return responses.SendOK(ctx, quotations, "Quotations retrieved successfully")
}

func HandleGetQuotation(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.Param("id")
	q, err := GetQuotationForUser(id, userID)
	if err != nil {
		return responses.SendError(ctx, 404, "Quotation not found")
	}
	return responses.SendOK(ctx, q, "Quotation retrieved successfully")
}

func HandleUpdateQuotation(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	id := ctx.Param("id")
	var req struct {
		Status string `json:"status"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}
	if err := ValidateUpdateQuotation(req.Status); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}
	updates := bson.M{"status": req.Status}
	updated, err := UpdateQuotationForUser(id, userID, updates)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to update quotation")
	}
	if !updated {
		return responses.SendError(ctx, 404, "Quotation not found")
	}
	return responses.SendOK(ctx, nil, "Quotation updated successfully")
}

func relatedQuotationFilter(userID string) bson.M {
	return bson.M{"$or": bson.A{
		bson.M{"customer_id": userID},
		bson.M{"artisan_id": userID},
	}}
}

func HandleCreateExternalQuotation(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req struct {
		CustomerEmail  string                       `json:"customer_email"`
		CustomerPhone  string                       `json:"customer_phone"`
		CustomerName   string                       `json:"customer_name"`
		JobTitle       string                       `json:"job_title"`
		JobDescription string                       `json:"job_description"`
		Items          []ExternalQuotationItemInput `json:"items"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateExternalQuotation(req.CustomerEmail, req.CustomerName, req.JobTitle, req.Items); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	eq := ExternalQuotation{
		ArtisanID:      userID,
		CustomerEmail:  req.CustomerEmail,
		CustomerPhone:  req.CustomerPhone,
		CustomerName:   req.CustomerName,
		Status:         "pending",
		JobTitle:       req.JobTitle,
		JobDescription: req.JobDescription,
	}
	for _, item := range req.Items {
		eq.Items = append(eq.Items, ExternalQuotationItem{
			Name:       item.Name,
			Quantity:   item.Quantity,
			Unit:       item.Unit,
			UnitPrice:  item.UnitPrice,
			TotalPrice: item.TotalPrice,
		})
		eq.TotalAmount += item.TotalPrice
	}

	if err := CreateExternalQuotation(&eq); err != nil {
		return responses.SendError(ctx, 500, "Failed to create external quotation")
	}

	notifyCustomerQuotation(&eq)

	return responses.SendCreated(ctx, eq, "External quotation created successfully")
}

func HandleListExternalQuotations(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	quotations, err := ListExternalQuotationsByArtisan(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch external quotations")
	}
	return responses.SendOK(ctx, quotations, "External quotations retrieved successfully")
}

func HandleGetExternalQuotation(ctx echo.Context) error {
	id := ctx.Param("id")
	eq, err := GetExternalQuotationByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "External quotation not found")
	}
	return responses.SendOK(ctx, eq, "External quotation retrieved successfully")
}

func HandleUpdateExternalQuotationStatus(ctx echo.Context) error {
	id := ctx.Param("id")
	action := ctx.Param("action")

	if action != "accepted" && action != "rejected" {
		return responses.SendError(ctx, 400, "Invalid action: must be accepted or rejected")
	}

	eq, err := GetExternalQuotationByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "External quotation not found")
	}

	if eq.Status != "pending" {
		return responses.SendError(ctx, 400, "Quotation has already been processed")
	}

	if err := UpdateExternalQuotation(id, map[string]interface{}{"status": action}); err != nil {
		return responses.SendError(ctx, 500, "Failed to update quotation status")
	}

	// A customer opening the link from email/SMS uses GET; send them to the
	// frontend confirmation page instead of returning raw JSON.
	if ctx.Request().Method == http.MethodGet {
		appURL := strings.TrimRight(configs.AppPublicURL(), "/")
		if appURL != "" {
			return ctx.Redirect(http.StatusFound, appURL+"/quotation-updated")
		}
	}

	return responses.SendOK(ctx, nil, "Quotation status updated to "+action)
}

func HandleCreateExternalInvoice(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req struct {
		CustomerEmail  string                       `json:"customer_email"`
		CustomerPhone  string                       `json:"customer_phone"`
		CustomerName   string                       `json:"customer_name"`
		JobTitle       string                       `json:"job_title"`
		JobDescription string                       `json:"job_description"`
		Items          []ExternalQuotationItemInput `json:"items"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateExternalInvoice(req.CustomerEmail, req.CustomerPhone, req.CustomerName, req.JobTitle, req.Items); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	eq := ExternalQuotation{
		ArtisanID:      userID,
		CustomerEmail:  req.CustomerEmail,
		CustomerPhone:  req.CustomerPhone,
		CustomerName:   req.CustomerName,
		Status:         "invoice",
		JobTitle:       req.JobTitle,
		JobDescription: req.JobDescription,
	}
	for _, item := range req.Items {
		eq.Items = append(eq.Items, ExternalQuotationItem{
			Name:       item.Name,
			Quantity:   item.Quantity,
			Unit:       item.Unit,
			UnitPrice:  item.UnitPrice,
			TotalPrice: item.TotalPrice,
		})
		eq.TotalAmount += item.TotalPrice
	}

	if err := CreateExternalQuotation(&eq); err != nil {
		return responses.SendError(ctx, 500, "Failed to create invoice quotation")
	}

	invoice := ExternalInvoice{
		QuotationID: eq.ID,
		ArtisanID:   userID,
		Status:      InvoiceStatusUnpaid,
		TotalAmount: eq.TotalAmount,
	}
	if err := CreateExternalInvoice(&invoice); err != nil {
		return responses.SendError(ctx, 500, "Failed to create invoice")
	}

	// Generate a payment link and notify the customer. If the payment provider
	// is not configured, the invoice is still created (unpaid) and the artisan
	// can resend the link later once payments are enabled.
	if link, err := createInvoicePaymentLink(&invoice, &eq); err != nil {
		slog.Warn("failed to create invoice payment link", "invoice_id", invoice.ID, "error", err)
	} else {
		notifyCustomerInvoice(&eq, link)
	}

	return responses.SendCreated(ctx, invoice, "Invoice created successfully")
}

func HandleListExternalInvoices(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	invoices, err := ListExternalInvoicesByArtisan(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch invoices")
	}
	return responses.SendOK(ctx, invoices, "Invoices retrieved successfully")
}

func HandleGetExternalInvoice(ctx echo.Context) error {
	id := ctx.Param("id")
	invoice, err := GetExternalInvoiceByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Invoice not found")
	}
	return responses.SendOK(ctx, invoice, "Invoice retrieved successfully")
}

func HandleResendInvoicePaymentLink(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	id := ctx.Param("id")
	invoice, err := GetExternalInvoiceByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Invoice not found")
	}
	if invoice.ArtisanID != userID {
		return responses.SendError(ctx, 404, "Invoice not found")
	}
	if invoice.Status == InvoiceStatusPaid {
		return responses.SendError(ctx, 400, "Invoice has already been paid")
	}

	eq, err := GetExternalQuotationByID(invoice.QuotationID)
	if err != nil {
		return responses.SendError(ctx, 404, "Associated quotation not found")
	}

	link, err := createInvoicePaymentLink(invoice, eq)
	if err != nil {
		return responses.SendError(ctx, 502, "Failed to generate payment link: "+err.Error())
	}
	notifyCustomerInvoice(eq, link)

	return responses.SendOK(ctx, map[string]string{"payment_link": link}, "Payment link sent to customer")
}

func HandleCreateExternalPaymentBill(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req struct {
		CustomerEmail string  `json:"customer_email"`
		CustomerPhone string  `json:"customer_phone"`
		CustomerName  string  `json:"customer_name"`
		Amount        float64 `json:"amount"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if err := ValidateExternalPaymentBill(req.CustomerEmail, req.CustomerPhone, req.CustomerName, req.Amount); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	bill := ExternalPaymentBill{
		ArtisanID:   userID,
		Status:      "UNPAID",
		TotalAmount: req.Amount,
	}
	if err := CreateExternalPaymentBill(&bill); err != nil {
		return responses.SendError(ctx, 500, "Failed to create payment bill")
	}

	return responses.SendCreated(ctx, bill, "Payment bill created successfully")
}

func HandleListExternalPaymentBills(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	bills, err := ListExternalPaymentBillsByArtisan(userID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch payment bills")
	}
	return responses.SendOK(ctx, bills, "Payment bills retrieved successfully")
}
