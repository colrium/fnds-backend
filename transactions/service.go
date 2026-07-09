package transactions

import (
	"fmt"
	"strings"
)

const (
	TypePayment   = "payment"
	TypeDeposit   = "deposit"
	TypeWithdrawal = "withdrawal"
	TypeRefund    = "refund"

	StatusPending   = "pending"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusRefunded  = "refunded"
)

func ValidateCreateTransaction(amount float64, txType, provider string) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}
	if strings.TrimSpace(txType) == "" {
		return fmt.Errorf("transaction type is required")
	}
	if strings.TrimSpace(provider) == "" {
		return fmt.Errorf("provider is required")
	}
	return nil
}

func ValidateCreateWalletTransaction(amount float64, txType string) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}
	validTypes := map[string]bool{TypeDeposit: true, TypeWithdrawal: true}
	if !validTypes[txType] {
		return fmt.Errorf("wallet transaction type must be deposit or withdrawal")
	}
	return nil
}

func IsValidTransactionStatus(status string) bool {
	valid := map[string]bool{StatusPending: true, StatusCompleted: true, StatusFailed: true, StatusRefunded: true}
	return valid[status]
}
