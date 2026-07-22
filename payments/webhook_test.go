package payments

import "testing"

func TestVerifyFlutterwaveWebhookSignature(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		provided   string
		want       bool
	}{
		{"matching", "secret-hash", "secret-hash", true},
		{"wrong", "secret-hash", "wrong", false},
		{"missing provided", "secret-hash", "", false},
		{"missing configured", "", "anything", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyFlutterwaveWebhookSignature(tt.configured, tt.provided); got != tt.want {
				t.Fatalf("VerifyFlutterwaveWebhookSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
