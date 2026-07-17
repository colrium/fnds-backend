package payments

import (
	"backend/transactions"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

// OnChargeCompleted is invoked when a Flutterwave charge completes successfully.
// It receives the tx_ref and provider status. Other packages (e.g. quotations)
// register a handler here to reconcile domain records without creating an
// import cycle. It may be nil if no handler is registered.
var OnChargeCompleted func(txRef, status string)

func RegisterRoutes(e *echo.Group) {
	e.POST("/flutterwave/webhook", HandleFlutterwaveWebhook)
	e.POST("/cellulant/webhook", HandleCellulantWebhook)
	e.POST("/cellulant/payment-link/webhook", HandleCellulantPaymentLinkWebhook)
	e.GET("/kopokopo/webhook", HandleKopoKopoWebhook)
	e.POST("/kopokopo/webhook", HandleKopoKopoWebhook)
}

func HandleFlutterwaveWebhook(ctx echo.Context) error {
	var payload map[string]interface{}
	if err := ctx.Bind(&payload); err != nil {
		return responses.SendError(ctx, 400, "Invalid payload")
	}

	event, _ := payload["event"].(string)
	if event == "charge.completed" {
		data, ok := payload["data"].(map[string]interface{})
		if ok {
			ref, _ := data["tx_ref"].(string)
			status, _ := data["status"].(string)
			_ = transactions.UpdateFlutterwaveTransaction(ref, bson.M{"status": status})
			if OnChargeCompleted != nil {
				OnChargeCompleted(ref, status)
			}
		}
	}

	return responses.SendOK(ctx, nil, "Webhook processed")
}

func HandleCellulantWebhook(ctx echo.Context) error {
	var payload map[string]interface{}
	if err := ctx.Bind(&payload); err != nil {
		return responses.SendError(ctx, 400, "Invalid payload")
	}
	return responses.SendOK(ctx, nil, "Webhook processed")
}

func HandleCellulantPaymentLinkWebhook(ctx echo.Context) error {
	var payload map[string]interface{}
	if err := ctx.Bind(&payload); err != nil {
		return responses.SendError(ctx, 400, "Invalid payload")
	}
	return responses.SendOK(ctx, nil, "Webhook processed")
}

func HandleKopoKopoWebhook(ctx echo.Context) error {
	var payload map[string]interface{}
	if err := ctx.Bind(&payload); err != nil {
		return responses.SendError(ctx, 400, "Invalid payload")
	}
	return responses.SendOK(ctx, nil, "Webhook processed")
}
