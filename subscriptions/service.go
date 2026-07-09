package subscriptions

import (
	"fmt"
	"strings"
	"time"
)

func ValidateCreateSubscription(planID string) error {
	if strings.TrimSpace(planID) == "" {
		return fmt.Errorf("plan ID is required")
	}
	return nil
}

func IsSubscriptionActive(sub *UserSubscription) bool {
	if sub == nil || !sub.IsActive {
		return false
	}
	if sub.EndDate != nil && sub.EndDate.Before(time.Now()) {
		return false
	}
	return true
}
