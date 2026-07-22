package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/configs"
	"backend/payments"
	"backend/referrals"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	StatusPending               = "pending"
	StatusActive                = "active"
	StatusFailed                = "failed"
	StatusExpired               = "expired"
	FreeMonthlyApplicationLimit = 3
)

var ErrApplicationLimitReached = errors.New("monthly application limit reached")

type UserSubscription struct {
	ID                 string     `json:"id" bson:"_id"`
	UserID             string     `json:"user_id" bson:"user_id"`
	SubscriptionPlanID string     `json:"subscription_plan_id" bson:"subscription_plan_id"`
	Audience           Audience   `json:"audience" bson:"audience"`
	Status             string     `json:"status" bson:"status"`
	PaymentReference   string     `json:"payment_reference" bson:"payment_reference"`
	ResourceID         string     `json:"resource_id,omitempty" bson:"resource_id,omitempty"`
	AmountKES          int64      `json:"amount" bson:"amount"`
	Currency           string     `json:"currency" bson:"currency"`
	StartDate          time.Time  `json:"start_date,omitempty" bson:"start_date,omitempty"`
	EndDate            *time.Time `json:"end_date,omitempty" bson:"end_date,omitempty"`
	IsActive           bool       `json:"is_active" bson:"is_active"`
	CreatedAt          time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at" bson:"updated_at"`
}

type applicationUsage struct {
	ID        string    `bson:"_id"`
	UserID    string    `bson:"user_id"`
	Period    string    `bson:"period"`
	Count     int       `bson:"count"`
	UpdatedAt time.Time `bson:"updated_at"`
}

type ApplicationReservation struct {
	kind       string
	usageID    string
	referralID string
}

func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("user_subscriptions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "payment_reference", Value: 1}},
			Options: options.Index().SetName("uniq_subscription_payment_reference").SetUnique(true),
		},
		{
			Keys: bson.D{
				{Key: "user_id", Value: 1},
				{Key: "status", Value: 1},
				{Key: "end_date", Value: 1},
			},
			Options: options.Index().SetName("active_user_subscriptions"),
		},
	})
	if err != nil {
		return fmt.Errorf("creating subscription indexes: %w", err)
	}
	return nil
}

func GetAllPlans() ([]SubscriptionPlan, error) {
	return Plans(), nil
}

func CreateUserSubscription(ctx context.Context, sub *UserSubscription) error {
	now := time.Now().UTC()
	sub.ID = uuid.New().String()
	sub.Status = StatusPending
	sub.IsActive = false
	sub.CreatedAt = now
	sub.UpdatedAt = now
	sub.PaymentReference = "CEL-SUB-" + sub.ID
	_, err := configs.MI.DB.Collection("user_subscriptions").InsertOne(ctx, sub)
	if err != nil {
		return fmt.Errorf("creating subscription purchase: %w", err)
	}
	return nil
}

func UpdateUserSubscription(ctx context.Context, id string, updates bson.M) error {
	updates["updated_at"] = time.Now().UTC()
	_, err := configs.MI.DB.Collection("user_subscriptions").UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": updates},
	)
	if err != nil {
		return fmt.Errorf("updating subscription: %w", err)
	}
	return nil
}

func GetActiveSubscriptions(
	ctx context.Context,
	userID string,
	at time.Time,
) ([]UserSubscription, error) {
	filter := bson.M{
		"user_id":   userID,
		"status":    StatusActive,
		"is_active": true,
		"$or": bson.A{
			bson.M{"end_date": bson.M{"$gt": at}},
			bson.M{"end_date": bson.M{"$exists": false}},
			bson.M{"end_date": nil},
		},
	}
	cursor, err := configs.MI.DB.Collection("user_subscriptions").Find(
		ctx,
		filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("finding active subscriptions: %w", err)
	}
	defer cursor.Close(ctx)

	subscriptions := []UserSubscription{}
	if err := cursor.All(ctx, &subscriptions); err != nil {
		return nil, fmt.Errorf("decoding active subscriptions: %w", err)
	}
	return subscriptions, nil
}

func GetSubscriptionHistory(ctx context.Context, userID string) ([]UserSubscription, error) {
	cursor, err := configs.MI.DB.Collection("user_subscriptions").Find(
		ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("finding subscription history: %w", err)
	}
	defer cursor.Close(ctx)

	history := []UserSubscription{}
	if err := cursor.All(ctx, &history); err != nil {
		return nil, fmt.Errorf("decoding subscription history: %w", err)
	}
	return history, nil
}

func HasEntitlement(
	ctx context.Context,
	userID string,
	entitlement Entitlement,
	at time.Time,
) (bool, error) {
	subscriptions, err := GetActiveSubscriptions(ctx, userID, at)
	if err != nil {
		return false, err
	}
	for _, subscription := range subscriptions {
		plan, ok := FindPlan(subscription.SubscriptionPlanID)
		if ok && plan.HasEntitlement(entitlement) {
			return true, nil
		}
	}
	return false, nil
}

func ActivatePurchase(ctx context.Context, event payments.CellulantPaymentEvent) error {
	if event.RequestStatusCode != 178 && event.RequestStatusCode != 188 {
		return nil
	}

	var purchase UserSubscription
	err := configs.MI.DB.Collection("user_subscriptions").
		FindOne(ctx, bson.M{"payment_reference": event.MerchantTransactionID}).
		Decode(&purchase)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finding subscription purchase: %w", err)
	}
	if purchase.Status != StatusPending && purchase.Status != StatusActive {
		return fmt.Errorf("subscription purchase is not pending")
	}
	if err := validateCellulantPayment(
		purchase,
		event,
		configs.CellulantServiceCode(),
	); err != nil {
		return err
	}

	plan, ok := FindPlan(purchase.SubscriptionPlanID)
	if !ok {
		return fmt.Errorf("subscription plan no longer exists")
	}

	now := time.Now().UTC()
	end := purchase.EndDate
	if purchase.Status == StatusPending {
		end = SubscriptionEnd(now, plan.DurationDays)
		result, err := configs.MI.DB.Collection("user_subscriptions").UpdateOne(
			ctx,
			bson.M{"_id": purchase.ID, "status": StatusPending},
			bson.M{"$set": bson.M{
				"status":     StatusActive,
				"is_active":  true,
				"start_date": now,
				"end_date":   end,
				"updated_at": now,
			}},
		)
		if err != nil {
			return fmt.Errorf("activating subscription: %w", err)
		}
		if result.MatchedCount == 0 {
			if err := configs.MI.DB.Collection("user_subscriptions").
				FindOne(ctx, bson.M{"_id": purchase.ID, "status": StatusActive}).
				Decode(&purchase); err != nil {
				return fmt.Errorf("subscription purchase changed during activation")
			}
			end = purchase.EndDate
		}
	}

	if !plan.RequiresResource {
		planIDs := PlanIDsInGroup(plan.Group)
		if len(planIDs) > 0 {
			_, err := configs.MI.DB.Collection("user_subscriptions").UpdateMany(
				ctx,
				bson.M{
					"_id":                  bson.M{"$ne": purchase.ID},
					"user_id":              purchase.UserID,
					"subscription_plan_id": bson.M{"$in": planIDs},
					"status":               StatusActive,
					"is_active":            true,
				},
				bson.M{"$set": bson.M{
					"status":     StatusExpired,
					"is_active":  false,
					"end_date":   now,
					"updated_at": now,
				}},
			)
			if err != nil {
				return fmt.Errorf("expiring superseded subscriptions: %w", err)
			}
		}
	}

	switch {
	case plan.HasEntitlement(EntitlementFeaturedJob):
		result, err := configs.MI.DB.Collection("job_vacancies").UpdateOne(
			ctx,
			bson.M{
				"_id":      purchase.ResourceID,
				"owner_id": purchase.UserID,
				"status":   "open",
			},
			bson.M{"$set": bson.M{
				"is_featured": true,
				"featured_at": now,
				"updated_at":  now,
			}},
		)
		if err != nil {
			return fmt.Errorf("featuring vacancy: %w", err)
		}
		if result.MatchedCount == 0 {
			return fmt.Errorf("featured vacancy is unavailable")
		}
	case plan.HasEntitlement(EntitlementVerifiedDiscoverable):
		if _, err := configs.MI.DB.Collection("users").UpdateOne(
			ctx,
			bson.M{"_id": purchase.UserID, "user_type": "artisan"},
			bson.M{"$set": bson.M{
				"is_verified":    true,
				"verified_until": end,
				"updated_at":     now,
			}},
		); err != nil {
			return fmt.Errorf("verifying worker: %w", err)
		}
	}

	return nil
}

func IsSubscriptionActive(sub *UserSubscription) bool {
	return IsSubscriptionActiveAt(sub, time.Now())
}

func IsSubscriptionActiveAt(sub *UserSubscription, at time.Time) bool {
	if sub == nil || sub.Status != StatusActive || !sub.IsActive {
		return false
	}
	return sub.EndDate == nil || sub.EndDate.After(at)
}

func ReserveApplication(
	ctx context.Context,
	userID string,
	at time.Time,
) (*ApplicationReservation, error) {
	unlimited, err := HasEntitlement(
		ctx,
		userID,
		EntitlementUnlimitedApplications,
		at,
	)
	if err != nil {
		return nil, err
	}
	if unlimited {
		return &ApplicationReservation{kind: "unlimited"}, nil
	}

	period := at.UTC().Format("2006-01")
	usageID := "applications:" + userID + ":" + period
	var usage applicationUsage
	err = configs.MI.DB.Collection("entitlement_usage").FindOneAndUpdate(
		ctx,
		bson.M{
			"_id": usageID,
			"$or": bson.A{
				bson.M{"count": bson.M{"$lt": FreeMonthlyApplicationLimit}},
				bson.M{"count": bson.M{"$exists": false}},
			},
		},
		bson.M{
			"$setOnInsert": bson.M{
				"user_id": userID,
				"period":  period,
			},
			"$set": bson.M{"updated_at": at.UTC()},
			"$inc": bson.M{"count": 1},
		},
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	).Decode(&usage)
	if err == nil {
		return &ApplicationReservation{kind: "free", usageID: usageID}, nil
	}
	if !mongo.IsDuplicateKeyError(err) && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("reserving free application: %w", err)
	}

	referralID, err := referrals.ConsumeApplicationCredit(ctx, userID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrApplicationLimitReached
	}
	if err != nil {
		return nil, err
	}
	return &ApplicationReservation{kind: "referral", referralID: referralID}, nil
}

func validateCellulantPayment(
	purchase UserSubscription,
	event payments.CellulantPaymentEvent,
	serviceCode string,
) error {
	if event.MerchantTransactionID != purchase.PaymentReference ||
		event.AccountNumber != purchase.PaymentReference ||
		event.AmountPaid != float64(purchase.AmountKES) ||
		!strings.EqualFold(event.CurrencyCode, purchase.Currency) ||
		event.ServiceCode != serviceCode {
		return fmt.Errorf("Cellulant payment does not match subscription purchase")
	}
	return nil
}

func (r *ApplicationReservation) Rollback(ctx context.Context) error {
	if r == nil {
		return nil
	}
	switch r.kind {
	case "free":
		_, err := configs.MI.DB.Collection("entitlement_usage").UpdateOne(
			ctx,
			bson.M{"_id": r.usageID, "count": bson.M{"$gt": 0}},
			bson.M{
				"$inc": bson.M{"count": -1},
				"$set": bson.M{"updated_at": time.Now().UTC()},
			},
		)
		if err != nil {
			return fmt.Errorf("rolling back free application: %w", err)
		}
	case "referral":
		return referrals.RestoreApplicationCredit(ctx, r.referralID)
	}
	return nil
}
