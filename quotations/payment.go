package quotations

import (
	"fmt"
	"log/slog"
	"strings"

	"backend/configs"
	"backend/payments"
	"backend/transactions"
)

// RegisterPaymentHooks wires the Flutterwave charge-completed callback so a
// paid invoice charge flips its invoice to paid. Call once at startup.
func RegisterPaymentHooks() {
	payments.RegisterChargeCompletedHandler(func(event payments.ChargeCompletedEvent) error {
		invoiceID := InvoiceIDFromTxRef(event.TxRef)
		if invoiceID == "" {
			return nil
		}
		if event.Status != "successful" && event.Status != "completed" {
			return nil
		}
		if err := UpdateExternalInvoice(invoiceID, map[string]interface{}{"status": InvoiceStatusPaid}); err != nil {
			slog.Error("failed to mark invoice paid from webhook", "invoice_id", invoiceID, "error", err)
			return err
		}
		return nil
	})
}

// invoiceTxPrefix marks a Flutterwave tx_ref as belonging to an invoice so the
// webhook can map a completed charge back to the invoice it paid.
const invoiceTxPrefix = "invoice:"

// InvoiceIDFromTxRef returns the invoice id encoded in a tx_ref, or "" if the
// ref does not belong to an invoice.
func InvoiceIDFromTxRef(txRef string) string {
	if !strings.HasPrefix(txRef, invoiceTxPrefix) {
		return ""
	}
	return strings.TrimPrefix(txRef, invoiceTxPrefix)
}

// createInvoicePaymentLink initiates a Flutterwave payment for an invoice and
// returns the hosted payment link. It also records a pending Flutterwave
// transaction keyed by the invoice tx_ref so the webhook can reconcile it.
func createInvoicePaymentLink(inv *ExternalInvoice, eq *ExternalQuotation) (string, error) {
	secret := configs.FlutterwaveSecretKey()
	if secret == "" {
		return "", fmt.Errorf("payment provider not configured")
	}

	txRef := invoiceTxPrefix + inv.ID
	redirectURL := strings.TrimRight(configs.AppPublicURL(), "/") + "/quotation-updated"

	fw := payments.NewFlutterwaveService(secret)
	resp, err := fw.CreatePayment(payments.FlutterwavePaymentRequest{
		TxRef:       txRef,
		Amount:      inv.TotalAmount,
		Currency:    "KES",
		RedirectURL: redirectURL,
		Customer: payments.FlutterwaveCustomer{
			Email:       eq.CustomerEmail,
			PhoneNumber: eq.CustomerPhone,
			Name:        eq.CustomerName,
		},
	})
	if err != nil {
		return "", err
	}
	if resp.Data.Link == "" {
		return "", fmt.Errorf("payment provider returned no link")
	}

	// Record the pending transaction so the webhook can reconcile against it.
	_ = transactions.CreateFlutterwaveTransaction(&transactions.FlutterwaveTransaction{
		TransactionRef: txRef,
		Amount:         inv.TotalAmount,
		Currency:       "KES",
		Status:         "pending",
		UserID:         inv.ArtisanID,
	})
	inv.PaymentLink = resp.Data.Link
	_ = UpdateExternalInvoice(inv.ID, map[string]interface{}{"payment_link": inv.PaymentLink})

	return resp.Data.Link, nil
}
