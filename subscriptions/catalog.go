package subscriptions

import "time"

type Audience string

const (
	AudienceEmployer Audience = "employer"
	AudienceWorker   Audience = "worker"
)

type Entitlement string

const (
	EntitlementBasicJobPosting       Entitlement = "basic_job_posting"
	EntitlementFeaturedJob           Entitlement = "featured_job"
	EntitlementBrowseWorkers         Entitlement = "browse_workers"
	EntitlementBasicApplications     Entitlement = "basic_applications"
	EntitlementUnlimitedApplications Entitlement = "unlimited_applications"
	EntitlementVerifiedDiscoverable  Entitlement = "verified_discoverable"
)

type SubscriptionPlan struct {
	ID           string        `json:"id" bson:"_id"`
	Name         string        `json:"name" bson:"name"`
	Description  string        `json:"description" bson:"description"`
	Audience     Audience      `json:"audience" bson:"audience"`
	PriceKES     int64         `json:"price" bson:"price"`
	Currency     string        `json:"currency" bson:"currency"`
	DurationDays int           `json:"duration" bson:"duration"`
	Features     []string      `json:"features" bson:"features"`
	Entitlements []Entitlement `json:"entitlements" bson:"entitlements"`
	IsDefault    bool          `json:"is_default" bson:"is_default"`
}

var planCatalog = []SubscriptionPlan{
	{
		ID:           "employer_free",
		Name:         "Employer Free",
		Description:  "Post standard jobs and vacancies at no cost.",
		Audience:     AudienceEmployer,
		Currency:     "KES",
		Features:     []string{"Free basic jobs and vacancy posting"},
		Entitlements: []Entitlement{EntitlementBasicJobPosting},
		IsDefault:    true,
	},
	{
		ID:           "employer_featured_job",
		Name:         "Featured Job",
		Description:  "Feature one job above standard listings until it closes.",
		Audience:     AudienceEmployer,
		PriceKES:     1000,
		Currency:     "KES",
		Features:     []string{"Feature one job at the top of vacancy listings"},
		Entitlements: []Entitlement{EntitlementFeaturedJob},
	},
	{
		ID:           "employer_browse_day",
		Name:         "Employer Daily Access",
		Description:  "Browse and contact unlimited worker profiles for one day.",
		Audience:     AudienceEmployer,
		PriceKES:     1000,
		Currency:     "KES",
		DurationDays: 1,
		Features:     []string{"Unlimited worker profile browsing and contact for one day"},
		Entitlements: []Entitlement{EntitlementBrowseWorkers},
	},
	{
		ID:           "employer_hiring_month",
		Name:         "Monthly Hiring",
		Description:  "Browse and contact unlimited worker profiles for 30 days.",
		Audience:     AudienceEmployer,
		PriceKES:     2000,
		Currency:     "KES",
		DurationDays: 30,
		Features:     []string{"Unlimited worker profile browsing and contact for 30 days"},
		Entitlements: []Entitlement{EntitlementBrowseWorkers},
	},
	{
		ID:           "worker_free",
		Name:         "Worker Free",
		Description:  "Apply to up to three vacancies every calendar month.",
		Audience:     AudienceWorker,
		Currency:     "KES",
		Features:     []string{"Three job applications every calendar month"},
		Entitlements: []Entitlement{EntitlementBasicApplications},
		IsDefault:    true,
	},
	{
		ID:           "worker_unlimited_day",
		Name:         "Unlimited Jobs Daily",
		Description:  "Unlimited job applications for one day.",
		Audience:     AudienceWorker,
		PriceKES:     100,
		Currency:     "KES",
		DurationDays: 1,
		Features:     []string{"Unlimited job applications for one day"},
		Entitlements: []Entitlement{EntitlementUnlimitedApplications},
	},
	{
		ID:           "worker_unlimited_week",
		Name:         "Unlimited Jobs Weekly",
		Description:  "Unlimited job applications for seven days.",
		Audience:     AudienceWorker,
		PriceKES:     350,
		Currency:     "KES",
		DurationDays: 7,
		Features:     []string{"Unlimited job applications for seven days", "Save 50%"},
		Entitlements: []Entitlement{EntitlementUnlimitedApplications},
	},
	{
		ID:           "worker_unlimited_month",
		Name:         "Unlimited Jobs Monthly",
		Description:  "Unlimited job applications for 30 days.",
		Audience:     AudienceWorker,
		PriceKES:     700,
		Currency:     "KES",
		DurationDays: 30,
		Features:     []string{"Unlimited job applications for 30 days", "Save 75%"},
		Entitlements: []Entitlement{EntitlementUnlimitedApplications},
	},
	{
		ID:           "worker_verified_month",
		Name:         "Verified and Discoverable",
		Description:  "Get verified and remain discoverable to employers for 30 days.",
		Audience:     AudienceWorker,
		PriceKES:     1000,
		Currency:     "KES",
		DurationDays: 30,
		Features:     []string{"Verified worker badge", "Discoverable in employer browse results"},
		Entitlements: []Entitlement{EntitlementVerifiedDiscoverable},
	},
}

func Plans() []SubscriptionPlan {
	plans := make([]SubscriptionPlan, len(planCatalog))
	copy(plans, planCatalog)
	return plans
}

func FindPlan(code string) (SubscriptionPlan, bool) {
	for _, plan := range planCatalog {
		if plan.ID == code {
			return plan, true
		}
	}
	return SubscriptionPlan{}, false
}

func (p SubscriptionPlan) HasEntitlement(entitlement Entitlement) bool {
	for _, item := range p.Entitlements {
		if item == entitlement {
			return true
		}
	}
	return false
}

func (p SubscriptionPlan) SupportsUserType(userType string) bool {
	switch p.Audience {
	case AudienceEmployer:
		return userType == "customer"
	case AudienceWorker:
		return userType == "artisan"
	default:
		return false
	}
}

func SubscriptionEnd(start time.Time, durationDays int) *time.Time {
	if durationDays <= 0 {
		return nil
	}
	end := start.Add(time.Duration(durationDays) * 24 * time.Hour)
	return &end
}
