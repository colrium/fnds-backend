package payments

import (
	"crypto/subtle"
	"net/http"

	"backend/configs"
	"backend/responses"
	"backend/transactions"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

type ChargeCompletedEvent struct {
	TxRef         string
	Status        string
	Amount        float64
	Currency      string
	TransactionID int
}

var chargeCompletedHandlers []func(ChargeCompletedEvent) error

func RegisterChargeCompletedHandler(handler func(ChargeCompletedEvent) error) {
	chargeCompletedHandlers = append(chargeCompletedHandlers, handler)
}

func VerifyFlutterwaveWebhookSignature(configured, provided string) bool {
	if configured == "" || provided == "" || len(configured) != len(provided) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(provided)) == 1
}

func RegisterRoutes(e *echo.Group) {
	e.POST("/flutterwave/webhook", HandleFlutterwaveWebhook)
	e.POST("/cellulant/webhook", HandleCellulantWebhook)
	e.POST("/cellulant/payment-link/webhook", HandleCellulantPaymentLinkWebhook)
	e.GET("/kopokopo/webhook", HandleKopoKopoWebhook)
	e.POST("/kopokopo/webhook", HandleKopoKopoWebhook)
}

func HandleFlutterwaveWebhook(ctx echo.Context) error {
	if !VerifyFlutterwaveWebhookSignature(
		configs.FlutterwaveWebhookSecretHash(),
		ctx.Request().Header.Get("verif-hash"),
	) {
		return responses.SendError(ctx, http.StatusUnauthorized, "Invalid webhook signature")
	}

	var payload struct {
		Event string `json:"event"`
		Data  struct {
			TxRef    string  `json:"tx_ref"`
			Status   string  `json:"status"`
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
			ID       int     `json:"id"`
		} `json:"data"`
	}
	if err := ctx.Bind(&payload); err != nil {
		return responses.SendError(ctx, 400, "Invalid payload")
	}

	if payload.Event == "charge.completed" {
		event := ChargeCompletedEvent{
			TxRef:         payload.Data.TxRef,
			Status:        payload.Data.Status,
			Amount:        payload.Data.Amount,
			Currency:      payload.Data.Currency,
			TransactionID: payload.Data.ID,
		}
		if err := transactions.UpdateFlutterwaveTransaction(
			event.TxRef,
			bson.M{"status": event.Status, "flutterwave_ref": event.TransactionID},
		); err != nil {
			return responses.SendError(ctx, http.StatusInternalServerError, "Failed to process webhook")
		}
		for _, handler := range chargeCompletedHandlers {
			if err := handler(event); err != nil {
				return responses.SendError(ctx, http.StatusInternalServerError, "Failed to process webhook")
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
