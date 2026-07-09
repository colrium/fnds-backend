package subscriptions

import (
	"testing"
	"time"
)

func TestValidateCreateSubscription(t *testing.T) {
	tests := []struct {
		name   string
		planID string
		wantOk bool
	}{
		{"valid plan ID", "plan-123", true},
		{"empty plan ID", "", false},
		{"whitespace plan ID", "   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateSubscription(tt.planID)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestIsSubscriptionActive(t *testing.T) {
	now := time.Now()
	future := now.Add(30 * 24 * time.Hour)
	past := now.Add(-30 * 24 * time.Hour)

	tests := []struct {
		name string
		sub  *UserSubscription
		want bool
	}{
		{"nil subscription", nil, false},
		{"inactive subscription", &UserSubscription{IsActive: false, EndDate: &future}, false},
		{"active with future end date", &UserSubscription{IsActive: true, EndDate: &future}, true},
		{"active with past end date", &UserSubscription{IsActive: true, EndDate: &past}, false},
		{"active with nil end date", &UserSubscription{IsActive: true, EndDate: nil}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSubscriptionActive(tt.sub); got != tt.want {
				t.Errorf("IsSubscriptionActive() = %v, want %v", got, tt.want)
			}
		})
	}
}
