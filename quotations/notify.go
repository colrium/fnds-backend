package quotations

import (
	"fmt"
	"log/slog"
	"strings"

	"backend/configs"
	"backend/notifications"
)

// notifyCustomerQuotation sends the customer the links to accept or reject a
// quotation. Failures are logged but never block quotation creation.
func notifyCustomerQuotation(eq *ExternalQuotation) {
	base := strings.TrimRight(configs.APIPublicURL(), "/")
	if base == "" {
		slog.Warn("API_PUBLIC_URL not set; skipping quotation customer notification", "quotation_id", eq.ID)
		return
	}

	acceptURL := fmt.Sprintf("%s/api/v1/external-quotations/%s/status/accepted", base, eq.ID)
	rejectURL := fmt.Sprintf("%s/api/v1/external-quotations/%s/status/rejected", base, eq.ID)

	emailBody := fmt.Sprintf(
		"Hello %s,\n\nYou have received a quotation from a Fundis artisan for \"%s\" totalling KES %.2f.\n\nReview and respond:\nAccept: %s\nReject: %s\n\nThank you,\nFundis",
		eq.CustomerName, eq.JobTitle, eq.TotalAmount, acceptURL, rejectURL,
	)
	if eq.CustomerEmail != "" {
		if err := notifications.NewEmailService("noreply@fundis.app").Send(notifications.EmailRequest{
			To:      eq.CustomerEmail,
			Subject: "You've received a quotation from Fundis",
			Body:    emailBody,
		}); err != nil {
			slog.Error("failed to send quotation email", "quotation_id", eq.ID, "error", err)
		}
	}

	if eq.CustomerPhone != "" {
		smsBody := fmt.Sprintf("Fundis: You have a quotation for \"%s\" (KES %.2f). Accept: %s", eq.JobTitle, eq.TotalAmount, acceptURL)
		if err := notifications.SendSMS(notifications.DefaultSMSProvider, eq.CustomerPhone, smsBody); err != nil {
			slog.Error("failed to send quotation SMS", "quotation_id", eq.ID, "error", err)
		}
	}
}

// notifyCustomerInvoice sends the customer a payment link for an invoice.
// Failures are logged but never block invoice creation.
func notifyCustomerInvoice(eq *ExternalQuotation, paymentLink string) {
	if paymentLink == "" {
		return
	}

	emailBody := fmt.Sprintf(
		"Hello %s,\n\nYou have an invoice from a Fundis artisan for \"%s\" totalling KES %.2f.\n\nPay securely here:\n%s\n\nThank you,\nFundis",
		eq.CustomerName, eq.JobTitle, eq.TotalAmount, paymentLink,
	)
	if eq.CustomerEmail != "" {
		if err := notifications.NewEmailService("noreply@fundis.app").Send(notifications.EmailRequest{
			To:      eq.CustomerEmail,
			Subject: "Your Fundis invoice is ready for payment",
			Body:    emailBody,
		}); err != nil {
			slog.Error("failed to send invoice email", "quotation_id", eq.ID, "error", err)
		}
	}

	if eq.CustomerPhone != "" {
		smsBody := fmt.Sprintf("Fundis: Invoice for \"%s\" (KES %.2f). Pay here: %s", eq.JobTitle, eq.TotalAmount, paymentLink)
		if err := notifications.SendSMS(notifications.DefaultSMSProvider, eq.CustomerPhone, smsBody); err != nil {
			slog.Error("failed to send invoice SMS", "quotation_id", eq.ID, "error", err)
		}
	}
}
