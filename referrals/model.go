package referrals

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	CodeLength               = 8
	RewardApplicationCredits = 3
	StatusPending            = "pending"
	StatusQualified          = "qualified"
)

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

var (
	ErrInvalidCode = errors.New("invalid referral code")
	ErrNotWorker   = errors.New("referral codes are only available to workers")
)

type Code struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	Code      string    `json:"code" bson:"code"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

type Referral struct {
	ID                     string     `json:"id" bson:"_id"`
	ReferrerID             string     `json:"referrer_id" bson:"referrer_id"`
	ReferredUserID         string     `json:"referred_user_id" bson:"referred_user_id"`
	Code                   string     `json:"code" bson:"code"`
	Status                 string     `json:"status" bson:"status"`
	ApplicationCredits     int        `json:"application_credits" bson:"application_credits"`
	ApplicationCreditsUsed int        `json:"application_credits_used" bson:"application_credits_used"`
	CreatedAt              time.Time  `json:"created_at" bson:"created_at"`
	QualifiedAt            *time.Time `json:"qualified_at,omitempty" bson:"qualified_at,omitempty"`
}

func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func GenerateCode() (string, error) {
	raw := make([]byte, CodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating referral code: %w", err)
	}
	for i := range raw {
		raw[i] = codeAlphabet[int(raw[i])%len(codeAlphabet)]
	}
	return string(raw), nil
}

func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("referral_codes").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().SetName("uniq_referral_code_user").SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "code", Value: 1}},
			Options: options.Index().SetName("uniq_referral_code").SetUnique(true),
		},
	})
	if err != nil {
		return fmt.Errorf("creating referral code indexes: %w", err)
	}
	_, err = configs.MI.DB.Collection("referrals").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "referred_user_id", Value: 1}},
			Options: options.Index().SetName("uniq_referred_user").SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "referrer_id", Value: 1}, {Key: "status", Value: 1}},
			Options: options.Index().SetName("referrer_status"),
		},
	})
	if err != nil {
		return fmt.Errorf("creating referral indexes: %w", err)
	}
	return nil
}

func GetOrCreateCode(ctx context.Context, userID, userType string) (*Code, error) {
	if userType != "artisan" {
		return nil, ErrNotWorker
	}
	var existing Code
	err := configs.MI.DB.Collection("referral_codes").
		FindOne(ctx, bson.M{"user_id": userID}).
		Decode(&existing)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("finding referral code: %w", err)
	}

	for attempt := 0; attempt < 5; attempt++ {
		value, err := GenerateCode()
		if err != nil {
			return nil, err
		}
		code := &Code{
			ID:        uuid.New().String(),
			UserID:    userID,
			Code:      value,
			CreatedAt: time.Now().UTC(),
		}
		_, err = configs.MI.DB.Collection("referral_codes").InsertOne(ctx, code)
		if err == nil {
			return code, nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return nil, fmt.Errorf("creating referral code: %w", err)
		}
		if findErr := configs.MI.DB.Collection("referral_codes").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&existing); findErr == nil {
			return &existing, nil
		}
	}
	return nil, fmt.Errorf("creating referral code: retry limit reached")
}

func AccountType(ctx context.Context, userID string) (string, error) {
	var account struct {
		UserType string `bson:"user_type"`
	}
	if err := configs.MI.DB.Collection("users").
		FindOne(ctx, bson.M{"_id": userID}).
		Decode(&account); err != nil {
		return "", fmt.Errorf("finding referral account: %w", err)
	}
	return account.UserType, nil
}

func ResolveCode(ctx context.Context, value string) (*Code, error) {
	var code Code
	err := configs.MI.DB.Collection("referral_codes").
		FindOne(ctx, bson.M{"code": NormalizeCode(value)}).
		Decode(&code)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrInvalidCode
	}
	if err != nil {
		return nil, fmt.Errorf("resolving referral code: %w", err)
	}

	var referrer struct {
		UserType string `bson:"user_type"`
		IsActive bool   `bson:"is_active"`
	}
	err = configs.MI.DB.Collection("users").
		FindOne(ctx, bson.M{"_id": code.UserID}).
		Decode(&referrer)
	if err != nil || referrer.UserType != "artisan" || !referrer.IsActive {
		return nil, ErrInvalidCode
	}
	return &code, nil
}

func CreatePending(ctx context.Context, code *Code, referredUserID string) error {
	if code == nil || referredUserID == "" || code.UserID == referredUserID {
		return ErrInvalidCode
	}
	referral := Referral{
		ID:             uuid.New().String(),
		ReferrerID:     code.UserID,
		ReferredUserID: referredUserID,
		Code:           code.Code,
		Status:         StatusPending,
		CreatedAt:      time.Now().UTC(),
	}
	_, err := configs.MI.DB.Collection("referrals").InsertOne(ctx, referral)
	if err != nil {
		return fmt.Errorf("creating referral: %w", err)
	}
	return nil
}

func EnsureQualified(ctx context.Context, codeValue, referredUserID string) error {
	code, err := ResolveCode(ctx, codeValue)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := configs.MI.DB.Collection("referrals").UpdateOne(
		ctx,
		bson.M{
			"referred_user_id": referredUserID,
			"referrer_id":      code.UserID,
			"status":           StatusPending,
		},
		bson.M{"$set": bson.M{
			"status":                   StatusQualified,
			"qualified_at":             now,
			"application_credits":      RewardApplicationCredits,
			"application_credits_used": 0,
		}},
	)
	if err != nil {
		return fmt.Errorf("qualifying referral: %w", err)
	}
	if result.MatchedCount > 0 {
		return nil
	}

	var existing Referral
	err = configs.MI.DB.Collection("referrals").
		FindOne(ctx, bson.M{"referred_user_id": referredUserID}).
		Decode(&existing)
	if err == nil {
		if existing.ReferrerID == code.UserID && existing.Status == StatusQualified {
			return nil
		}
		return ErrInvalidCode
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return fmt.Errorf("checking referral qualification: %w", err)
	}

	referral := Referral{
		ID:                     uuid.New().String(),
		ReferrerID:             code.UserID,
		ReferredUserID:         referredUserID,
		Code:                   code.Code,
		Status:                 StatusQualified,
		ApplicationCredits:     RewardApplicationCredits,
		ApplicationCreditsUsed: 0,
		CreatedAt:              now,
		QualifiedAt:            &now,
	}
	if _, err := configs.MI.DB.Collection("referrals").InsertOne(ctx, referral); err != nil {
		return fmt.Errorf("creating qualified referral: %w", err)
	}
	return nil
}

func ConsumeApplicationCredit(ctx context.Context, referrerID string) (string, error) {
	var referral Referral
	err := configs.MI.DB.Collection("referrals").FindOneAndUpdate(
		ctx,
		bson.M{
			"referrer_id": referrerID,
			"status":      StatusQualified,
			"$expr": bson.M{"$lt": bson.A{
				"$application_credits_used",
				"$application_credits",
			}},
		},
		bson.M{"$inc": bson.M{"application_credits_used": 1}},
		options.FindOneAndUpdate().
			SetSort(bson.D{{Key: "qualified_at", Value: 1}}).
			SetReturnDocument(options.After),
	).Decode(&referral)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", mongo.ErrNoDocuments
	}
	if err != nil {
		return "", fmt.Errorf("consuming referral credit: %w", err)
	}
	return referral.ID, nil
}

func RestoreApplicationCredit(ctx context.Context, referralID string) error {
	_, err := configs.MI.DB.Collection("referrals").UpdateOne(
		ctx,
		bson.M{"_id": referralID, "application_credits_used": bson.M{"$gt": 0}},
		bson.M{"$inc": bson.M{"application_credits_used": -1}},
	)
	if err != nil {
		return fmt.Errorf("restoring referral credit: %w", err)
	}
	return nil
}

func Stats(ctx context.Context, referrerID string) (map[string]int64, error) {
	qualified, err := configs.MI.DB.Collection("referrals").CountDocuments(
		ctx,
		bson.M{"referrer_id": referrerID, "status": StatusQualified},
	)
	if err != nil {
		return nil, fmt.Errorf("counting qualified referrals: %w", err)
	}
	pending, err := configs.MI.DB.Collection("referrals").CountDocuments(
		ctx,
		bson.M{"referrer_id": referrerID, "status": StatusPending},
	)
	if err != nil {
		return nil, fmt.Errorf("counting pending referrals: %w", err)
	}
	var credits struct {
		Available int64 `bson:"available"`
	}
	cursor, err := configs.MI.DB.Collection("referrals").Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{
			"referrer_id": referrerID,
			"status":      StatusQualified,
		}}},
		bson.D{{Key: "$group", Value: bson.M{
			"_id": nil,
			"available": bson.M{"$sum": bson.M{"$subtract": bson.A{
				"$application_credits",
				"$application_credits_used",
			}}},
		}}},
	})
	if err != nil {
		return nil, fmt.Errorf("counting referral credits: %w", err)
	}
	defer cursor.Close(ctx)
	if cursor.Next(ctx) {
		if err := cursor.Decode(&credits); err != nil {
			return nil, fmt.Errorf("decoding referral credits: %w", err)
		}
	}
	return map[string]int64{
		"qualified":                     qualified,
		"pending":                       pending,
		"application_credits_available": credits.Available,
	}, nil
}
