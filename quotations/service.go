package quotations

import (
	"fmt"
	"regexp"
	"strings"
)

func ValidateCreateQuotation(jobRequestID string, totalAmount float64) error {
	if strings.TrimSpace(jobRequestID) == "" {
		return fmt.Errorf("job request ID is required")
	}
	if totalAmount <= 0 {
		return fmt.Errorf("total amount must be greater than zero")
	}
	return nil
}

func ValidateQuotationItem(description string, quantity int, unitPrice float64) error {
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("item description is required")
	}
	if quantity <= 0 {
		return fmt.Errorf("quantity must be greater than zero")
	}
	if unitPrice <= 0 {
		return fmt.Errorf("unit price must be greater than zero")
	}
	return nil
}

func ValidateUpdateQuotation(status string) error {
	validStatuses := map[string]bool{
		"pending":   true,
		"accepted":  true,
		"rejected":  true,
		"cancelled": true,
	}
	if !validStatuses[status] {
		return fmt.Errorf("invalid status: must be one of pending, accepted, rejected, cancelled")
	}
	return nil
}

func ValidateExternalQuotation(customerEmail, customerName, jobTitle string, items []ExternalQuotationItemInput) error {
	if strings.TrimSpace(customerEmail) == "" {
		return fmt.Errorf("customer email is required")
	}
	if !regexp.MustCompile(`^[^@]+@[^@]+\.[^@]+$`).MatchString(customerEmail) {
		return fmt.Errorf("invalid customer email format")
	}
	if strings.TrimSpace(customerName) == "" {
		return fmt.Errorf("customer name is required")
	}
	if strings.TrimSpace(jobTitle) == "" {
		return fmt.Errorf("job title is required")
	}
	if len(items) == 0 {
		return fmt.Errorf("at least one item is required")
	}
	hasServiceCharge := false
	for i, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("item %d: name is required", i+1)
		}
		if item.Quantity <= 0 {
			return fmt.Errorf("item %d: quantity must be greater than zero", i+1)
		}
		if item.UnitPrice <= 0 {
			return fmt.Errorf("item %d: unit price must be greater than zero", i+1)
		}
		if item.TotalPrice <= 0 {
			return fmt.Errorf("item %d: total price must be greater than zero", i+1)
		}
		if item.IsServiceCharge {
			hasServiceCharge = true
		}
	}
	if !hasServiceCharge {
		return fmt.Errorf("at least one item must be a service charge")
	}
	return nil
}

func ValidateExternalInvoice(customerEmail, customerPhone, customerName, jobTitle string, items []ExternalQuotationItemInput) error {
	if err := ValidateExternalQuotation(customerEmail, customerName, jobTitle, items); err != nil {
		return err
	}
	phoneRegex := regexp.MustCompile(`^(\+254)\d{9}$`)
	if strings.TrimSpace(customerPhone) == "" {
		return fmt.Errorf("customer phone is required for invoice")
	}
	if !phoneRegex.MatchString(customerPhone) {
		return fmt.Errorf("customer phone must be in format +254XXXXXXXXX")
	}
	return nil
}

func ValidateExternalPaymentBill(customerEmail, customerPhone, customerName string, amount float64) error {
	if strings.TrimSpace(customerEmail) == "" {
		return fmt.Errorf("customer email is required")
	}
	if !regexp.MustCompile(`^[^@]+@[^@]+\.[^@]+$`).MatchString(customerEmail) {
		return fmt.Errorf("invalid customer email format")
	}
	phoneRegex := regexp.MustCompile(`^(\+254)\d{9}$`)
	if strings.TrimSpace(customerPhone) == "" {
		return fmt.Errorf("customer phone is required")
	}
	if !phoneRegex.MatchString(customerPhone) {
		return fmt.Errorf("customer phone must be in format +254XXXXXXXXX")
	}
	if strings.TrimSpace(customerName) == "" {
		return fmt.Errorf("customer name is required")
	}
	if amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}
	return nil
}

type ExternalQuotationItemInput struct {
	Name            string  `json:"name"`
	Quantity        int     `json:"quantity"`
	Unit            string  `json:"unit"`
	UnitPrice       float64 `json:"unit_price"`
	TotalPrice      float64 `json:"total_price"`
	Description     string  `json:"description"`
	IsServiceCharge bool    `json:"is_service_charge"`
}
