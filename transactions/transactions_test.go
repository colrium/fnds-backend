package transactions

import (
	"testing"
)

func TestValidateCreateTransaction(t *testing.T) {
	tests := []struct {
		name     string
		amount   float64
		txType   string
		provider string
		wantErr  bool
		errMsg   string
	}{
		{"valid transaction", 50000, "payment", "flutterwave", false, ""},
		{"zero amount", 0, "payment", "flutterwave", true, "amount must be greater than zero"},
		{"negative amount", -100, "payment", "flutterwave", true, "amount must be greater than zero"},
		{"empty type", 50000, "", "flutterwave", true, "transaction type is required"},
		{"empty provider", 50000, "payment", "", true, "provider is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateTransaction(tt.amount, tt.txType, tt.provider)
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

func TestValidateCreateWalletTransaction(t *testing.T) {
	tests := []struct {
		name    string
		amount  float64
		txType  string
		wantErr bool
		errMsg  string
	}{
		{"valid deposit", 50000, "deposit", false, ""},
		{"valid withdrawal", 10000, "withdrawal", false, ""},
		{"zero amount", 0, "deposit", true, "amount must be greater than zero"},
		{"invalid type", 50000, "refund", true, "wallet transaction type must be deposit or withdrawal"},
		{"empty type", 50000, "", true, "wallet transaction type must be deposit or withdrawal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateWalletTransaction(tt.amount, tt.txType)
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

func TestIsValidTransactionStatus(t *testing.T) {
	tests := []struct {
		status string
		valid  bool
	}{
		{"pending", true},
		{"completed", true},
		{"failed", true},
		{"refunded", true},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := IsValidTransactionStatus(tt.status); got != tt.valid {
				t.Errorf("IsValidTransactionStatus(%q) = %v, want %v", tt.status, got, tt.valid)
			}
		})
	}
}
