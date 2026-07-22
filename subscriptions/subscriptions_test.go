package subscriptions

import (
	"testing"
	"time"

	"backend/payments"
)

func TestPlanCatalogMatchesPublishedPricing(t *testing.T) {
	tests := []struct {
		code         string
		audience     Audience
		priceKES     int64
		durationDays int
		entitlement  Entitlement
	}{
		{"employer_free", AudienceEmployer, 0, 0, EntitlementBasicJobPosting},
		{"employer_featured_job", AudienceEmployer, 1000, 0, EntitlementFeaturedJob},
		{"employer_browse_day", AudienceEmployer, 1000, 1, EntitlementBrowseWorkers},
		{"employer_hiring_month", AudienceEmployer, 2000, 30, EntitlementBrowseWorkers},
		{"worker_free", AudienceWorker, 0, 0, EntitlementBasicApplications},
		{"worker_unlimited_day", AudienceWorker, 100, 1, EntitlementUnlimitedApplications},
		{"worker_unlimited_week", AudienceWorker, 350, 7, EntitlementUnlimitedApplications},
		{"worker_unlimited_month", AudienceWorker, 700, 30, EntitlementUnlimitedApplications},
		{"worker_verified_month", AudienceWorker, 1000, 30, EntitlementVerifiedDiscoverable},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			plan, ok := FindPlan(tt.code)
			if !ok {
				t.Fatalf("FindPlan(%q) not found", tt.code)
			}
			if plan.Audience != tt.audience {
				t.Errorf("Audience = %q, want %q", plan.Audience, tt.audience)
			}
			if plan.PriceKES != tt.priceKES {
				t.Errorf("PriceKES = %d, want %d", plan.PriceKES, tt.priceKES)
			}
			if plan.DurationDays != tt.durationDays {
				t.Errorf("DurationDays = %d, want %d", plan.DurationDays, tt.durationDays)
			}
			if !plan.HasEntitlement(tt.entitlement) {
				t.Errorf("plan does not grant %q", tt.entitlement)
			}
		})
	}
}

func TestPlanSupportsUserType(t *testing.T) {
	tests := []struct {
		name     string
		planCode string
		userType string
		want     bool
	}{
		{"worker plan for artisan", "worker_unlimited_day", "artisan", true},
		{"worker plan for customer", "worker_unlimited_day", "customer", false},
		{"employer plan for customer", "employer_hiring_month", "customer", true},
		{"employer plan for artisan", "employer_hiring_month", "artisan", false},
		{"unknown user type", "worker_free", "admin", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, ok := FindPlan(tt.planCode)
			if !ok {
				t.Fatalf("FindPlan(%q) not found", tt.planCode)
			}
			if got := plan.SupportsUserType(tt.userType); got != tt.want {
				t.Errorf("SupportsUserType(%q) = %v, want %v", tt.userType, got, tt.want)
			}
		})
	}
}

func TestSubscriptionPeriod(t *testing.T) {
	start := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		durationDays int
		wantEnd      *time.Time
	}{
		{"one day", 1, timePointer(start.Add(24 * time.Hour))},
		{"one week", 7, timePointer(start.Add(7 * 24 * time.Hour))},
		{"one month", 30, timePointer(start.Add(30 * 24 * time.Hour))},
		{"one-time feature", 0, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubscriptionEnd(start, tt.durationDays)
			if tt.wantEnd == nil {
				if got != nil {
					t.Fatalf("SubscriptionEnd() = %v, want nil", got)
				}
				return
			}
			if got == nil || !got.Equal(*tt.wantEnd) {
				t.Fatalf("SubscriptionEnd() = %v, want %v", got, tt.wantEnd)
			}
		})
	}
}

func TestIsSubscriptionActive(t *testing.T) {
	now := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		sub  *UserSubscription
		want bool
	}{
		{"nil", nil, false},
		{"pending", &UserSubscription{Status: StatusPending}, false},
		{"active future", &UserSubscription{Status: StatusActive, IsActive: true, EndDate: &future}, true},
		{"active expired", &UserSubscription{Status: StatusActive, IsActive: true, EndDate: &past}, false},
		{"active one-time", &UserSubscription{Status: StatusActive, IsActive: true}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSubscriptionActiveAt(tt.sub, now); got != tt.want {
				t.Errorf("IsSubscriptionActiveAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateCellulantPayment(t *testing.T) {
	purchase := UserSubscription{
		PaymentReference: "CEL-SUB-123",
		AmountKES:        350,
		Currency:         "KES",
	}
	valid := payments.CellulantPaymentEvent{
		MerchantTransactionID: "CEL-SUB-123",
		AccountNumber:         "CEL-SUB-123",
		AmountPaid:            350,
		CurrencyCode:          "kes",
		ServiceCode:           "FUNDIS",
	}
	if err := validateCellulantPayment(purchase, valid, "FUNDIS"); err != nil {
		t.Fatalf("valid Cellulant payment rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*payments.CellulantPaymentEvent)
	}{
		{"merchant reference", func(event *payments.CellulantPaymentEvent) {
			event.MerchantTransactionID = "CEL-SUB-other"
		}},
		{"account reference", func(event *payments.CellulantPaymentEvent) {
			event.AccountNumber = "CEL-SUB-other"
		}},
		{"amount", func(event *payments.CellulantPaymentEvent) { event.AmountPaid = 100 }},
		{"currency", func(event *payments.CellulantPaymentEvent) { event.CurrencyCode = "USD" }},
		{"service code", func(event *payments.CellulantPaymentEvent) {
			event.ServiceCode = "OTHER"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := valid
			tt.mutate(&event)
			if err := validateCellulantPayment(purchase, event, "FUNDIS"); err == nil {
				t.Fatal("mismatched Cellulant payment accepted")
			}
		})
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
