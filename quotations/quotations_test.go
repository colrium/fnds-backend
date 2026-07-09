package quotations

import (
	"testing"
)

func TestValidateCreateQuotation(t *testing.T) {
	tests := []struct {
		name          string
		jobRequestID  string
		totalAmount   float64
		wantErr       bool
		errMsg        string
	}{
		{"valid quotation", "jr-123", 50000, false, ""},
		{"empty job request ID", "", 50000, true, "job request ID is required"},
		{"zero amount", "jr-123", 0, true, "total amount must be greater than zero"},
		{"negative amount", "jr-123", -100, true, "total amount must be greater than zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateQuotation(tt.jobRequestID, tt.totalAmount)
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

func TestValidateQuotationItem(t *testing.T) {
	tests := []struct {
		name        string
		description string
		quantity    int
		unitPrice   float64
		wantErr     bool
		errMsg      string
	}{
		{"valid item", "Labor cost", 1, 25000, false, ""},
		{"empty description", "", 1, 25000, true, "item description is required"},
		{"zero quantity", "Labor cost", 0, 25000, true, "quantity must be greater than zero"},
		{"zero unit price", "Labor cost", 1, 0, true, "unit price must be greater than zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateQuotationItem(tt.description, tt.quantity, tt.unitPrice)
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

func TestValidateExternalQuotation(t *testing.T) {
	validItems := []ExternalQuotationItemInput{
		{Name: "Service fee", Quantity: 1, UnitPrice: 50000, TotalPrice: 50000, IsServiceCharge: true},
	}

	tests := []struct {
		name          string
		customerEmail string
		customerName  string
		jobTitle      string
		items         []ExternalQuotationItemInput
		wantErr       bool
		errMsg        string
	}{
		{"valid external quotation", "customer@example.com", "John Doe", "Plumbing", validItems, false, ""},
		{"empty email", "", "John Doe", "Plumbing", validItems, true, "customer email is required"},
		{"invalid email", "invalid", "John Doe", "Plumbing", validItems, true, "invalid customer email format"},
		{"empty name", "customer@example.com", "", "Plumbing", validItems, true, "customer name is required"},
		{"empty job title", "customer@example.com", "John Doe", "", validItems, true, "job title is required"},
		{"no items", "customer@example.com", "John Doe", "Plumbing", []ExternalQuotationItemInput{}, true, "at least one item is required"},
		{"item empty name", "customer@example.com", "John Doe", "Plumbing", []ExternalQuotationItemInput{
			{Name: "", Quantity: 1, UnitPrice: 100, TotalPrice: 100, IsServiceCharge: true},
		}, true, "item 1: name is required"},
		{"item zero quantity", "customer@example.com", "John Doe", "Plumbing", []ExternalQuotationItemInput{
			{Name: "Fee", Quantity: 0, UnitPrice: 100, TotalPrice: 100, IsServiceCharge: true},
		}, true, "item 1: quantity must be greater than zero"},
		{"item zero unit price", "customer@example.com", "John Doe", "Plumbing", []ExternalQuotationItemInput{
			{Name: "Fee", Quantity: 1, UnitPrice: 0, TotalPrice: 100, IsServiceCharge: true},
		}, true, "item 1: unit price must be greater than zero"},
		{"item zero total price", "customer@example.com", "John Doe", "Plumbing", []ExternalQuotationItemInput{
			{Name: "Fee", Quantity: 1, UnitPrice: 100, TotalPrice: 0, IsServiceCharge: true},
		}, true, "item 1: total price must be greater than zero"},
		{"no service charge", "customer@example.com", "John Doe", "Plumbing", []ExternalQuotationItemInput{
			{Name: "Fee", Quantity: 1, UnitPrice: 100, TotalPrice: 100, IsServiceCharge: false},
		}, true, "at least one item must be a service charge"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalQuotation(tt.customerEmail, tt.customerName, tt.jobTitle, tt.items)
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

func TestValidateExternalInvoice(t *testing.T) {
	validItems := []ExternalQuotationItemInput{
		{Name: "Service fee", Quantity: 1, UnitPrice: 50000, TotalPrice: 50000, IsServiceCharge: true},
	}

	tests := []struct {
		name          string
		customerEmail string
		customerPhone string
		customerName  string
		jobTitle      string
		items         []ExternalQuotationItemInput
		wantErr       bool
		errMsg        string
	}{
		{"valid invoice", "customer@example.com", "+254712345678", "John Doe", "Plumbing", validItems, false, ""},
		{"empty phone", "customer@example.com", "", "John Doe", "Plumbing", validItems, true, "customer phone is required for invoice"},
		{"invalid phone format", "customer@example.com", "0712345678", "John Doe", "Plumbing", validItems, true, "customer phone must be in format +254XXXXXXXXX"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalInvoice(tt.customerEmail, tt.customerPhone, tt.customerName, tt.jobTitle, tt.items)
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

func TestValidateExternalPaymentBill(t *testing.T) {
	tests := []struct {
		name          string
		customerEmail string
		customerPhone string
		customerName  string
		amount        float64
		wantErr       bool
		errMsg        string
	}{
		{"valid payment bill", "customer@example.com", "+254712345678", "John Doe", 50000, false, ""},
		{"empty email", "", "+254712345678", "John Doe", 50000, true, "customer email is required"},
		{"invalid email", "invalid", "+254712345678", "John Doe", 50000, true, "invalid customer email format"},
		{"empty phone", "customer@example.com", "", "John Doe", 50000, true, "customer phone is required"},
		{"invalid phone", "customer@example.com", "0712345678", "John Doe", 50000, true, "customer phone must be in format +254XXXXXXXXX"},
		{"empty name", "customer@example.com", "+254712345678", "", 50000, true, "customer name is required"},
		{"zero amount", "customer@example.com", "+254712345678", "John Doe", 0, true, "amount must be greater than zero"},
		{"negative amount", "customer@example.com", "+254712345678", "John Doe", -100, true, "amount must be greater than zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalPaymentBill(tt.customerEmail, tt.customerPhone, tt.customerName, tt.amount)
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

func TestValidateUpdateQuotation(t *testing.T) {
	tests := []struct {
		name   string
		status string
		wantOk bool
	}{
		{"pending", "pending", true},
		{"accepted", "accepted", true},
		{"rejected", "rejected", true},
		{"cancelled", "cancelled", true},
		{"invalid", "unknown", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUpdateQuotation(tt.status)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error for status %q, got %v", tt.status, err)
			}
			if !tt.wantOk && err == nil {
				t.Errorf("expected error for status %q, got nil", tt.status)
			}
		})
	}
}
