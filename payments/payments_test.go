package payments

import (
	"testing"
)

func TestValidateWebhookPayload(t *testing.T) {
	tests := []struct {
		name     string
		payload  map[string]interface{}
		provider PaymentProvider
		wantErr  bool
		errMsg   string
	}{
		{"nil payload", nil, ProviderCellulant, true, "payload is required"},
		{"cellulant valid", map[string]interface{}{"transaction_id": "tx-123"}, ProviderCellulant, false, ""},
		{"cellulant missing id", map[string]interface{}{"other": "value"}, ProviderCellulant, true, "missing transaction_id field"},
		{"kopokopo valid", map[string]interface{}{"event_type": "payment_received"}, ProviderKopoKopo, false, ""},
		{"kopokopo missing event_type", map[string]interface{}{"other": "value"}, ProviderKopoKopo, true, "missing event_type field"},
		{"unknown provider", map[string]interface{}{}, "unknown", true, "unknown payment provider: unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhookPayload(tt.payload, tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSanitizeProvider(t *testing.T) {
	tests := []struct {
		input string
		want  PaymentProvider
	}{
		{"cellulant", ProviderCellulant},
		{"CELLULANT", ProviderCellulant},
		{"kopokopo", ProviderKopoKopo},
		{"KOPOKOPO", ProviderKopoKopo},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := SanitizeProvider(tt.input); got != tt.want {
				t.Errorf("SanitizeProvider(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
