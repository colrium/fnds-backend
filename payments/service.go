package payments

import (
	"fmt"
	"strings"
)

type PaymentProvider string

const (
	ProviderCellulant PaymentProvider = "cellulant"
	ProviderKopoKopo  PaymentProvider = "kopokopo"
)

type PaymentResult struct {
	Success   bool   `json:"success"`
	Reference string `json:"reference,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

func ValidateWebhookPayload(payload map[string]interface{}, provider PaymentProvider) error {
	if payload == nil {
		return fmt.Errorf("payload is required")
	}
	switch provider {
	case ProviderCellulant:
		if _, ok := payload["transaction_id"]; !ok {
			return fmt.Errorf("missing transaction_id field")
		}
	case ProviderKopoKopo:
		if _, ok := payload["event_type"]; !ok {
			return fmt.Errorf("missing event_type field")
		}
	default:
		return fmt.Errorf("unknown payment provider: %s", provider)
	}
	return nil
}

func SanitizeProvider(provider string) PaymentProvider {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "cellulant":
		return ProviderCellulant
	case "kopokopo":
		return ProviderKopoKopo
	default:
		return PaymentProvider(p)
	}
}
