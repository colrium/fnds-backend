package banks

import (
	"testing"
)

func TestCreateBankValidation(t *testing.T) {
	tests := []struct {
		name     string
		bankName string
		code     string
		wantErr  bool
		errMsg   string
	}{
		{"empty name", "", "BK001", true, "bank name is required"},
		{"empty code", "Bank of Kigali", "", true, "bank code is required"},
		{"whitespace name", "   ", "BK001", true, "bank name is required"},
		{"whitespace code", "Bank of Kigali", "   ", true, "bank code is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CreateBankWithValidation(tt.bankName, tt.code)
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
