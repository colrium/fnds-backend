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

	cellulantConfig := payments.CellulantConfig{
		AccessKey:   configs.CellulantAccessKey(),
		IVKey:       configs.CellulantIVKey(),
		SecretKey:   configs.CellulantSecretKey(),
		ServiceCode: configs.CellulantServiceCode(),
		CheckoutURL: configs.CellulantCheckoutURL(),
	}
	if cellulantConfig.AccessKey == "" ||
		cellulantConfig.IVKey == "" ||
		cellulantConfig.SecretKey == "" ||
		cellulantConfig.ServiceCode == "" ||
		cellulantConfig.CheckoutURL == "" {
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

	transaction := &transactions.CellulantTransaction{
		TransactionID: purchase.PaymentReference,
		Amount:        float64(plan.PriceKES),
		Status:        StatusPending,
		UserID:        user.ID,
	}
	if err := transactions.CreateCellulantTransaction(transaction); err != nil {
		_ = UpdateUserSubscription(ctx, purchase.ID, bson.M{"status": StatusFailed})
		return nil, fmt.Errorf("recording subscription transaction: %w", err)
	}

	redirectURL := strings.TrimRight(configs.AppPublicURL(), "/") + "/subscriptions"
	callbackURL := strings.TrimRight(configs.APIPublicURL(), "/") + "/api/v1/cellulant/webhook"
	checkoutURL, err := payments.NewCellulantService(cellulantConfig).CheckoutURL(
		payments.CellulantCheckoutRequest{
			MSISDN:                user.Phone,
			LanguageCode:          "en",
			RaiseInvoice:          false,
			RequestAmount:         float64(plan.PriceKES),
			RequestDescription:    plan.Name,
			CallbackURL:           callbackURL,
			FailRedirectURL:       redirectURL,
			PendingRedirectURL:    redirectURL,
			SuccessRedirectURL:    redirectURL,
			MerchantTransactionID: purchase.PaymentReference,
			ServiceCode:           cellulantConfig.ServiceCode,
			DueDate:               time.Now().UTC().Add(4 * time.Hour).Format("2006-01-02 15:04:05"),
			AccountNumber:         purchase.PaymentReference,
			CurrencyCode:          plan.Currency,
			CountryCode:           "KEN",
			CustomerFirstName:     user.FirstName,
			CustomerLastName:      user.LastName,
			CustomerEmail:         user.Email,
		},
	)
	if err != nil {
		_ = UpdateUserSubscription(ctx, purchase.ID, bson.M{"status": StatusFailed})
		return nil, fmt.Errorf("creating subscription checkout: %w", err)
	}

	return &Checkout{Subscription: purchase, PaymentLink: checkoutURL}, nil
}

func RegisterPaymentHooks() {
	payments.RegisterCellulantPaymentHandler(func(event payments.CellulantPaymentEvent) error {
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
