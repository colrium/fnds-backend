package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/configs"
	"backend/payments"
	"backend/transactions"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
)

var (
	ErrPlanNotFound        = errors.New("subscription plan not found")
	ErrPlanRoleMismatch    = errors.New("subscription plan is not available for this account")
	ErrFreePlan            = errors.New("free plan benefits are applied automatically")
	ErrResourceRequired    = errors.New("resource_id is required for a featured job")
	ErrResourceUnavailable = errors.New("job vacancy not found")
	ErrPaymentUnavailable  = errors.New("payment provider is not configured")
)

type PurchaseRequest struct {
	PlanID     string `json:"plan_id"`
	ResourceID string `json:"resource_id,omitempty"`
}

type Checkout struct {
	Subscription UserSubscription `json:"subscription"`
	PaymentLink  string           `json:"payment_link"`
}

func StartCheckout(
	ctx context.Context,
	user *users.User,
	request PurchaseRequest,
) (*Checkout, error) {
	plan, ok := FindPlan(strings.TrimSpace(request.PlanID))
	if !ok {
		return nil, ErrPlanNotFound
	}
	if !plan.SupportsUserType(user.UserType) {
		return nil, ErrPlanRoleMismatch
	}
	if plan.PriceKES == 0 {
		return nil, ErrFreePlan
	}
	if plan.HasEntitlement(EntitlementFeaturedJob) {
		if strings.TrimSpace(request.ResourceID) == "" {
			return nil, ErrResourceRequired
		}
		count, err := configs.MI.DB.Collection("job_vacancies").CountDocuments(
			ctx,
			bson.M{
				"_id":      request.ResourceID,
				"owner_id": user.ID,
				"status":   "open",
			},
		)
		if err != nil {
			return nil, fmt.Errorf("checking featured vacancy: %w", err)
		}
		if count != 1 {
			return nil, ErrResourceUnavailable
		}
	}

	secret := configs.FlutterwaveSecretKey()
	if secret == "" {
		return nil, ErrPaymentUnavailable
	}

	purchase := UserSubscription{
		UserID:             user.ID,
		SubscriptionPlanID: plan.ID,
		Audience:           plan.Audience,
		ResourceID:         strings.TrimSpace(request.ResourceID),
		AmountKES:          plan.PriceKES,
		Currency:           plan.Currency,
	}
	if err := CreateUserSubscription(ctx, &purchase); err != nil {
		return nil, err
	}

	transaction := &transactions.FlutterwaveTransaction{
		TransactionRef: purchase.PaymentReference,
		Amount:         float64(plan.PriceKES),
		Currency:       plan.Currency,
		Status:         StatusPending,
		UserID:         user.ID,
	}
	if err := transactions.CreateFlutterwaveTransaction(transaction); err != nil {
		_ = UpdateUserSubscription(ctx, purchase.ID, bson.M{"status": StatusFailed})
		return nil, fmt.Errorf("recording subscription transaction: %w", err)
	}

	redirectURL := strings.TrimRight(configs.AppPublicURL(), "/") + "/subscriptions"
	response, err := payments.NewFlutterwaveService(secret).CreatePayment(
		payments.FlutterwavePaymentRequest{
			TxRef:          purchase.PaymentReference,
			Amount:         float64(plan.PriceKES),
			Currency:       plan.Currency,
			RedirectURL:    redirectURL,
			PaymentOptions: "mpesa",
			Customer: payments.FlutterwaveCustomer{
				Email:       user.Email,
				PhoneNumber: user.Phone,
				Name:        strings.TrimSpace(user.FirstName + " " + user.LastName),
			},
		},
	)
	if err != nil {
		_ = UpdateUserSubscription(ctx, purchase.ID, bson.M{"status": StatusFailed})
		return nil, fmt.Errorf("creating subscription checkout: %w", err)
	}
	if response.Data.Link == "" {
		_ = UpdateUserSubscription(ctx, purchase.ID, bson.M{"status": StatusFailed})
		return nil, fmt.Errorf("creating subscription checkout: payment provider returned no link")
	}

	return &Checkout{Subscription: purchase, PaymentLink: response.Data.Link}, nil
}

func RegisterPaymentHooks() {
	payments.RegisterChargeCompletedHandler(func(event payments.ChargeCompletedEvent) error {
		return ActivatePurchase(context.Background(), event)
	})
}

type AccountSummary struct {
	Subscriptions []UserSubscription `json:"subscriptions"`
	Entitlements  []Entitlement      `json:"entitlements"`
}

func GetAccountSummary(
	ctx context.Context,
	userID string,
	at time.Time,
) (*AccountSummary, error) {
	active, err := GetActiveSubscriptions(ctx, userID, at)
	if err != nil {
		return nil, err
	}
	seen := map[Entitlement]bool{}
	entitlements := []Entitlement{}
	for _, subscription := range active {
		plan, ok := FindPlan(subscription.SubscriptionPlanID)
		if !ok {
			continue
		}
		for _, entitlement := range plan.Entitlements {
			if !seen[entitlement] {
				seen[entitlement] = true
				entitlements = append(entitlements, entitlement)
			}
		}
	}
	return &AccountSummary{
		Subscriptions: active,
		Entitlements:  entitlements,
	}, nil
}
