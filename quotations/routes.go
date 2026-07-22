package quotations

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend/configs"
	"backend/contextutil"
	"backend/jobrequests"
	"backend/notifications"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func RegisterAuthRoutes(g *echo.Group) {
	g.POST("/quotations", HandleCreateQuotation)
	g.GET("/quotations", HandleListQuotations)
	g.GET("/quotations/:id", HandleGetQuotation)
	g.PATCH("/quotations/:id", HandleUpdateQuotation)
	g.GET("/quotations/:id/invoice", HandleGetQuotationInvoice)

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

	job, err := jobrequests.GetJobRequestByID(q.JobRequestID)
	if err != nil {
		return responses.SendError(ctx, 404, "Job request not found")
	}
	if job.ArtisanID != userID {
		return responses.SendError(ctx, 403, "You can only quote on jobs assigned to you")
	}
	if len(q.Items) == 0 {
		return responses.SendError(ctx, 400, "At least one quotation item is required")
	}

	q.ArtisanID = userID
	q.CustomerID = job.CustomerID
	q.Status = "pending"
	q.TotalAmount = 0
	for i := range q.Items {
		item := &q.Items[i]
		if err := ValidateQuotationItem(item.Description, item.Quantity, item.UnitPrice); err != nil {
			return responses.SendError(ctx, 400, err.Error())
		}
		item.ID = ""
		item.TotalPrice = float64(item.Quantity) * item.UnitPrice
		q.TotalAmount += item.TotalPrice
	}
	if err := ValidateCreateQuotation(q.JobRequestID, q.TotalAmount); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	if err := CreateQuotation(&q); err != nil {
		return responses.SendError(ctx, 500, "Failed to create quotation")
	}
	notifyInternalQuotationCreated(&q, job)
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
	q, err := GetQuotationByID(id)
	if err != nil {
		return responses.SendError(ctx, 404, "Quotation not found")
	}
	if q.CustomerID != userID {
		return responses.SendError(ctx, 403, "Only the customer can respond to a quotation")
	}
	if q.Status != "pending" {
		return responses.SendError(ctx, 400, "Quotation has already been processed")
	}
	updates := bson.M{"status": req.Status}
	updated, err := UpdateQuotationForUser(id, userID, updates)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to update quotation")
	}
	if !updated {
		return responses.SendError(ctx, 404, "Quotation not found")
	}
	q.Status = req.Status
	if req.Status == "accepted" {
		invoice, paymentLink, err := ensureInternalQuotationInvoice(q)
		if err != nil {
			return responses.SendError(ctx, 500, "Quotation accepted but invoice could not be prepared")
		}
		notifyInternalQuotationDecision(q, "accepted")
		return responses.SendOK(ctx, map[string]interface{}{
			"quotation":    q,
			"invoice":      invoice,
			"payment_link": paymentLink,
		}, "Quotation updated successfully")
	}
	notifyInternalQuotationDecision(q, req.Status)
	return responses.SendOK(ctx, q, "Quotation updated successfully")
}

func HandleGetQuotationInvoice(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	q, err := GetQuotationForUser(ctx.Param("id"), userID)
	if err != nil || q.Status != "accepted" {
		return responses.SendError(ctx, 404, "Invoice not found")
	}
	invoice, _, err := ensureInternalQuotationInvoice(q)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to load invoice")
	}
	return responses.SendOK(ctx, invoice, "Invoice retrieved successfully")
}

func notifyInternalQuotationCreated(q *Quotation, job *jobrequests.JobRequest) {
	customer, err := users.GetUserByID(q.CustomerID)
	if err != nil {
		slog.Warn("could not load quotation customer for notification", "quotation_id", q.ID, "error", err)
		return
	}
	artisan, err := users.GetUserByID(q.ArtisanID)
	if err != nil {
		slog.Warn("could not load quotation artisan for notification", "quotation_id", q.ID, "error", err)
		return
	}
	err = notifications.NewDefaultService().NotifyQuotationReceived(context.Background(), notifications.QuotationReceivedEvent{
		CustomerID:    q.CustomerID,
		CustomerEmail: customer.Email,
		ArtisanName:   strings.TrimSpace(artisan.FirstName + " " + artisan.LastName),
		QuotationID:   q.ID,
		JobRequestID:  q.JobRequestID,
		JobTitle:      job.Title,
		TotalAmount:   q.TotalAmount,
	})
	if err != nil {
		slog.Warn("failed to create quotation notification", "quotation_id", q.ID, "error", err)
	}
}

func notifyInternalQuotationDecision(q *Quotation, status string) {
	customer, err := users.GetUserByID(q.CustomerID)
	if err != nil {
		return
	}
	artisan, err := users.GetUserByID(q.ArtisanID)
	if err != nil {
		return
	}
	_ = notifications.NewDefaultService().NotifyQuotationDecision(context.Background(), notifications.QuotationDecisionEvent{
		ArtisanID:    q.ArtisanID,
		ArtisanEmail: artisan.Email,
		CustomerName: strings.TrimSpace(customer.FirstName + " " + customer.LastName),
		QuotationID:  q.ID,
		JobTitle:     "job quotation",
		Status:       status,
	})
}

func ensureInternalQuotationInvoice(q *Quotation) (*ExternalInvoice, string, error) {
	invoice, err := GetExternalInvoiceByQuotationID(q.ID)
	if err != nil {
		invoice = &ExternalInvoice{
			QuotationID: q.ID,
			ArtisanID:   q.ArtisanID,
			Status:      InvoiceStatusUnpaid,
			TotalAmount: q.TotalAmount,
			DueDate:     time.Now().AddDate(0, 0, 7).Format(time.RFC3339),
		}
		if err := CreateExternalInvoice(invoice); err != nil {
			if !mongo.IsDuplicateKeyError(err) {
				return nil, "", err
			}
			invoice, err = GetExternalInvoiceByQuotationID(q.ID)
			if err != nil {
				return nil, "", err
			}
		}
	}
	if invoice.Status == InvoiceStatusPaid || invoice.PaymentLink != "" {
		return invoice, invoice.PaymentLink, nil
	}
	customer, err := users.GetUserByID(q.CustomerID)
	if err != nil {
		return invoice, "", err
	}
	eq := &ExternalQuotation{
		ID:            q.ID,
		ArtisanID:     q.ArtisanID,
		CustomerName:  strings.TrimSpace(customer.FirstName + " " + customer.LastName),
		CustomerEmail: customer.Email,
		CustomerPhone: customer.Phone,
		JobTitle:      "Job quotation",
		TotalAmount:   q.TotalAmount,
	}
	link, err := createInvoicePaymentLink(invoice, eq)
	if err != nil {
		slog.Warn("failed to create internal quotation payment link", "quotation_id", q.ID, "error", err)
		return invoice, "", nil
	}
	notifyCustomerInvoice(eq, link)
	return invoice, link, nil
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
			Name:        item.Name,
			Quantity:    item.Quantity,
			Unit:        item.Unit,
			UnitPrice:   item.UnitPrice,
			TotalPrice:  float64(item.Quantity) * item.UnitPrice,
			Description: item.Description,
		})
		eq.TotalAmount += float64(item.Quantity) * item.UnitPrice
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

	if eq.Status == "accepted" && action == "accepted" {
		invoice, link, err := ensureAcceptedQuotationInvoice(eq)
		if err != nil {
			return responses.SendError(ctx, 500, "Failed to prepare invoice")
		}
		return respondToExternalQuotationAction(ctx, action, invoice, link)
	}
	if eq.Status != "pending" {
		return responses.SendError(ctx, 400, "Quotation has already been processed")
	}

	if err := UpdateExternalQuotation(id, map[string]interface{}{"status": action}); err != nil {
		return responses.SendError(ctx, 500, "Failed to update quotation status")
	}
	if action == "accepted" {
		invoice, link, err := ensureAcceptedQuotationInvoice(eq)
		if err != nil {
			return responses.SendError(ctx, 500, "Failed to prepare invoice")
		}
		return respondToExternalQuotationAction(ctx, action, invoice, link)
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

func ensureAcceptedQuotationInvoice(eq *ExternalQuotation) (*ExternalInvoice, string, error) {
	invoice, err := GetExternalInvoiceByQuotationID(eq.ID)
	if err != nil {
		invoice = &ExternalInvoice{
			QuotationID: eq.ID,
			ArtisanID:   eq.ArtisanID,
			Status:      InvoiceStatusUnpaid,
			TotalAmount: eq.TotalAmount,
			DueDate:     time.Now().AddDate(0, 0, 7).Format(time.RFC3339),
		}
		if err := CreateExternalInvoice(invoice); err != nil {
			if !mongo.IsDuplicateKeyError(err) {
				return nil, "", err
			}
			invoice, err = GetExternalInvoiceByQuotationID(eq.ID)
			if err != nil {
				return nil, "", err
			}
		}
	}
	if invoice.Status == InvoiceStatusPaid {
		return invoice, "", nil
	}
	if invoice.PaymentLink != "" {
		return invoice, invoice.PaymentLink, nil
	}
	link, err := createInvoicePaymentLink(invoice, eq)
	if err != nil {
		slog.Warn("failed to create accepted quotation payment link", "invoice_id", invoice.ID, "error", err)
		return invoice, "", nil
	}
	notifyCustomerInvoice(eq, link)
	return invoice, link, nil
}

func respondToExternalQuotationAction(ctx echo.Context, action string, invoice *ExternalInvoice, paymentLink string) error {
	if ctx.Request().Method == http.MethodGet {
		if paymentLink != "" {
			return ctx.Redirect(http.StatusFound, paymentLink)
		}
		appURL := strings.TrimRight(configs.AppPublicURL(), "/")
		if appURL != "" {
			return ctx.Redirect(http.StatusFound, appURL+"/quotation-updated?status="+action+"&invoice_id="+invoice.ID)
		}
	}
	return responses.SendOK(ctx, map[string]interface{}{
		"status":       action,
		"invoice":      invoice,
		"payment_link": paymentLink,
	}, "Quotation status updated to "+action)
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
			Name:        item.Name,
			Quantity:    item.Quantity,
			Unit:        item.Unit,
			UnitPrice:   item.UnitPrice,
			TotalPrice:  float64(item.Quantity) * item.UnitPrice,
			Description: item.Description,
		})
		eq.TotalAmount += float64(item.Quantity) * item.UnitPrice
	}

	if err := CreateExternalQuotation(&eq); err != nil {
		return responses.SendError(ctx, 500, "Failed to create invoice quotation")
	}

	invoice := ExternalInvoice{
		QuotationID: eq.ID,
		ArtisanID:   userID,
		Status:      InvoiceStatusUnpaid,
		TotalAmount: eq.TotalAmount,
		DueDate:     time.Now().AddDate(0, 0, 7).Format(time.RFC3339),
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
