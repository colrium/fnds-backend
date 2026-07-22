package payments

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

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
	var payload struct {
		CheckoutRequestID          interface{} `json:"checkoutRequestID"`
		CheckoutRequestIDSnake     interface{} `json:"checkout_request_id"`
		MerchantTransactionID      string      `json:"merchantTransactionID"`
		MerchantTransactionIDSnake string      `json:"merchant_transaction_id"`
		AccountNumber              string      `json:"accountNumber"`
		AccountNumberSnake         string      `json:"account_number"`
		RequestStatusCode          interface{} `json:"requestStatusCode"`
		RequestStatusCodeSnake     interface{} `json:"request_status_code"`
		RequestStatusDescription   string      `json:"requestStatusDescription"`
		RequestStatusDescSnake     string      `json:"request_status_description"`
		AmountPaid                 interface{} `json:"amountPaid"`
		AmountPaidSnake            interface{} `json:"amount_paid"`
		CurrencyCode               string      `json:"currencyCode"`
		CurrencyCodeSnake          string      `json:"currency_code"`
		ServiceCode                string      `json:"serviceCode"`
		ServiceCodeSnake           string      `json:"service_code"`
	}
	if err := ctx.Bind(&payload); err != nil {
		return responses.SendError(ctx, 400, "Invalid payload")
	}

	event := CellulantPaymentEvent{
		CheckoutRequestID: firstString(
			payload.CheckoutRequestID,
			payload.CheckoutRequestIDSnake,
		),
		MerchantTransactionID: firstNonEmpty(
			payload.MerchantTransactionID,
			payload.MerchantTransactionIDSnake,
		),
		AccountNumber: firstNonEmpty(payload.AccountNumber, payload.AccountNumberSnake),
		RequestStatusCode: firstInt(
			payload.RequestStatusCode,
			payload.RequestStatusCodeSnake,
		),
		RequestStatusDescription: firstNonEmpty(
			payload.RequestStatusDescription,
			payload.RequestStatusDescSnake,
		),
		AmountPaid:   firstFloat(payload.AmountPaid, payload.AmountPaidSnake),
		CurrencyCode: firstNonEmpty(payload.CurrencyCode, payload.CurrencyCodeSnake),
		ServiceCode:  firstNonEmpty(payload.ServiceCode, payload.ServiceCodeSnake),
	}
	if event.MerchantTransactionID == "" {
		event.MerchantTransactionID = event.AccountNumber
	}
	if event.MerchantTransactionID == "" || event.RequestStatusCode == 0 {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid Cellulant payload")
	}

	status := StatusFromCellulantCode(event.RequestStatusCode)
	if err := transactions.UpdateCellulantTransaction(
		event.MerchantTransactionID,
		bson.M{
			"status":              status,
			"checkout_request_id": event.CheckoutRequestID,
		},
	); err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to process webhook")
	}
	for _, handler := range cellulantPaymentHandlers {
		if err := handler(event); err != nil {
			return responses.SendError(ctx, http.StatusInternalServerError, "Failed to process webhook")
		}
	}

	acknowledgementCode := 180
	if status == "successful" || status == "pending" {
		acknowledgementCode = 183
	}
	return ctx.JSON(http.StatusOK, map[string]interface{}{
		"checkoutRequestID":     event.CheckoutRequestID,
		"merchantTransactionID": event.MerchantTransactionID,
		"statusCode":            acknowledgementCode,
		"statusDescription":     "Payment processed successfully",
		"receiptNumber":         event.AccountNumber,
	})
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

func StatusFromCellulantCode(code int) string {
	switch code {
	case 178, 188:
		return "successful"
	case 130, 177, 183:
		return "pending"
	case 129:
		return "expired"
	default:
		return "failed"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstString(values ...interface{}) string {
	for _, value := range values {
		if value != nil {
			return fmt.Sprint(value)
		}
	}
	return ""
}

func firstInt(values ...interface{}) int {
	for _, value := range values {
		switch typed := value.(type) {
		case int:
			if typed != 0 {
				return typed
			}
		case float64:
			if typed != 0 {
				return int(typed)
			}
		case json.Number:
			parsed, err := strconv.Atoi(typed.String())
			if err == nil && parsed != 0 {
				return parsed
			}
		case string:
			parsed, err := strconv.Atoi(typed)
			if err == nil && parsed != 0 {
				return parsed
			}
		}
	}
	return 0
}

func firstFloat(values ...interface{}) float64 {
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			if typed != 0 {
				return typed
			}
		case json.Number:
			parsed, err := typed.Float64()
			if err == nil && parsed != 0 {
				return parsed
			}
		case string:
			parsed, err := strconv.ParseFloat(typed, 64)
			if err == nil && parsed != 0 {
				return parsed
			}
		}
	}
	return 0
}
