package users

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusSuspended = "suspended"

	// ProfileStatus values gate whether an artisan may start an assessment
	// journey or apply for jobs. See backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md.
	ProfileStatusNotSubmitted  = "not_submitted"
	ProfileStatusPendingReview = "pending_review"
	ProfileStatusApproved      = "approved"
	ProfileStatusRejected      = "rejected"
)

type User struct {
	ID                  string     `json:"id" bson:"_id"`
	OldID               int        `json:"old_id,omitempty" bson:"old_id,omitempty"`
	FirstName           string     `json:"first_name" bson:"first_name"`
	LastName            string     `json:"last_name" bson:"last_name"`
	Email               string     `json:"email" bson:"email"`
	Phone               string     `json:"phone" bson:"phone"`
	Password            []byte     `json:"-" bson:"password"`
	UserType            string     `json:"user_type" bson:"user_type"`
	Status              string     `json:"status" bson:"status"`
	IsActive            bool       `json:"is_active" bson:"is_active"`
	Otp                 string     `json:"-" bson:"otp,omitempty"`
	OtpExpiresAt        time.Time  `json:"-" bson:"otp_expires_at,omitempty"`
	OtpAttempts         int        `json:"-" bson:"otp_attempts,omitempty"`
	PhoneVerified       bool       `json:"phone_verified" bson:"phone_verified"`
	FundisID            string     `json:"fundis_id,omitempty" bson:"fundis_id,omitempty"`
	AvatarID            string     `json:"avatar_id,omitempty" bson:"avatar_id,omitempty"`
	FCMToken            string     `json:"fcm_token,omitempty" bson:"fcm_token,omitempty"`
	IsAvailable         bool       `json:"is_available" bson:"is_available"`
	IsVerified          bool       `json:"is_verified" bson:"is_verified"`
	VerifiedUntil       *time.Time `json:"verified_until,omitempty" bson:"verified_until,omitempty"`
	CertifiedAt         *time.Time `json:"certified_at,omitempty" bson:"certified_at,omitempty"`
	CertificationGrade  int        `json:"certification_grade,omitempty" bson:"certification_grade,omitempty"`
	FeaturedUntil       *time.Time `json:"featured_until,omitempty" bson:"featured_until,omitempty"`
	Slug                string     `json:"slug,omitempty" bson:"slug,omitempty"`
	IsProfilePublic     bool       `json:"is_profile_public" bson:"is_profile_public"`
	ReferralCodeUsed    string     `json:"-" bson:"referral_code_used,omitempty"`
	Migrated            bool       `json:"migrated,omitempty" bson:"migrated,omitempty"`
	ProfileStatus       string     `json:"profile_status" bson:"profile_status"`
	ProfileSubmittedAt  *time.Time `json:"profile_submitted_at,omitempty" bson:"profile_submitted_at,omitempty"`
	ProfileReviewedAt   *time.Time `json:"profile_reviewed_at,omitempty" bson:"profile_reviewed_at,omitempty"`
	ProfileReviewedBy   string     `json:"profile_reviewed_by,omitempty" bson:"profile_reviewed_by,omitempty"`
	ProfileRejectReason string     `json:"profile_reject_reason,omitempty" bson:"profile_reject_reason,omitempty"`
	CreatedAt           time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at" bson:"updated_at"`
}

type ArtisanSummary struct {
	ID          string `json:"id"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	FundisID    string `json:"fundis_id,omitempty"`
	AvatarID    string `json:"avatar_id,omitempty"`
	IsAvailable bool   `json:"is_available"`
	IsVerified  bool   `json:"is_verified"`
	IsCertified bool   `json:"is_certified"`
	IsFeatured  bool   `json:"is_featured"`
}

func CreateNewUser(user *User) error {
	user.ID = uuid.New().String()
	user.CreatedAt = time.Now()
	user.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("users").InsertOne(context.TODO(), user)
	return err
}

// EnsureIndexes creates the unique constraints for the users collection.
// Phone and email are globally unique: a phone number or email belongs to
// exactly one account regardless of user_type. Email is enforced only when
// present, since it is optional at registration.
func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("users").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "phone", Value: 1}},
			Options: options.Index().SetName("uniq_phone").SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "email", Value: 1}},
			Options: options.Index().
				SetName("uniq_email").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"email": bson.M{"$type": "string", "$gt": ""}}),
		},
		{
			Keys: bson.D{{Key: "slug", Value: 1}},
			Options: options.Index().
				SetName("uniq_slug_public").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"slug":              bson.M{"$type": "string", "$gt": ""},
					"is_profile_public": true,
				}),
		},
	})
	return err
}

func GetUserByID(ctx context.Context, id string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(ctx, bson.M{"_id": id}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserBySlug(ctx context.Context, slug string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(ctx, bson.M{
		"slug":              slug,
		"is_profile_public": true,
		"user_type":         "artisan",
	}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUsersByIDs(ids []string) ([]User, error) {
	users := []User{}
	if len(ids) == 0 {
		return users, nil
	}
	cursor, err := configs.MI.DB.Collection("users").Find(context.TODO(), bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(context.TODO()) }()
	if err := cursor.All(context.TODO(), &users); err != nil {
		return nil, err
	}
	return users, nil
}

func GetUserByEmail(email string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"email": email}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByPhone(phone string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"phone": phone}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByEmailAndType(email, userType string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"email": email, "user_type": userType}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByPhoneAndType(phone, userType string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"phone": phone, "user_type": userType}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByFundisID(fundisID string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(context.TODO(), bson.M{"fundis_id": fundisID, "user_type": "artisan"}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetPublicUserByFundisID looks up a public artisan by their fundis_id.
// Used for fundis_id → slug redirects on public endpoints.
func GetPublicUserByFundisID(ctx context.Context, fundisID string) (*User, error) {
	var user User
	err := configs.MI.DB.Collection("users").FindOne(ctx, bson.M{
		"fundis_id":         fundisID,
		"is_profile_public": true,
		"user_type":         "artisan",
	}).Decode(&user)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func GetArtisanSummaryByFundisID(fundisID string) (*ArtisanSummary, error) {
	user, err := GetUserByFundisID(fundisID)
	if err != nil {
		return nil, err
	}
	summary := artisanSummary(*user, time.Now().UTC())
	return &summary, nil
}

func UpdateUser(id string, update bson.M) error {
	update["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("users").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": update})
	return err
}

func IncrementOtpAttempts(id string) error {
	_, err := configs.MI.DB.Collection("users").UpdateOne(context.TODO(),
		bson.M{"_id": id},
		bson.M{"$inc": bson.M{"otp_attempts": 1}, "$set": bson.M{"updated_at": time.Now()}})
	return err
}

func ListArtisans(page, limit int64) ([]User, int64, error) {
	filter := bson.M{
		"user_type":   "artisan",
		"is_verified": true,
	}

	// Sort by featured_until DESC (nulls last), then certified_at DESC, then created_at DESC.
	// MongoDB sorts null/missing fields first in ascending order and last in descending order,
	// so -1 naturally pushes nulls to the end.
	sortOrder := bson.D{
		{Key: "featured_until", Value: -1},
		{Key: "certified_at", Value: -1},
		{Key: "created_at", Value: -1},
	}

	opts := options.Find().
		SetSkip((page - 1) * limit).
		SetLimit(limit).
		SetSort(sortOrder)
	cursor, err := configs.MI.DB.Collection("users").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = cursor.Close(context.TODO()) }()

	var users []User
	if err := cursor.All(context.TODO(), &users); err != nil {
		return nil, 0, err
	}

	total, err := configs.MI.DB.Collection("users").CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func ListArtisanSummaries(page, limit int64) ([]ArtisanSummary, int64, error) {
	artisans, total, err := ListArtisans(page, limit)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().UTC()
	summaries := make([]ArtisanSummary, 0, len(artisans))
	for _, artisan := range artisans {
		summaries = append(summaries, artisanSummary(artisan, now))
	}
	return summaries, total, nil
}

func artisanSummary(user User, at time.Time) ArtisanSummary {
	verified := user.IsVerified && user.VerifiedUntil != nil && user.VerifiedUntil.After(at)
	certified := user.CertifiedAt != nil
	featured := user.FeaturedUntil != nil && user.FeaturedUntil.After(at)
	return ArtisanSummary{
		ID:          user.ID,
		FirstName:   user.FirstName,
		LastName:    user.LastName,
		FundisID:    user.FundisID,
		AvatarID:    user.AvatarID,
		IsAvailable: user.IsAvailable,
		IsVerified:  verified,
		IsCertified: certified,
		IsFeatured:  featured,
	}
}

func ListAllUsers(filter bson.M, page, limit int64) ([]User, int64, error) {
	opts := options.Find().SetSkip((page - 1) * limit).SetLimit(limit)
	cursor, err := configs.MI.DB.Collection("users").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = cursor.Close(context.TODO()) }()

	var userList []User
	if err := cursor.All(context.TODO(), &userList); err != nil {
		return nil, 0, err
	}

	total, err := configs.MI.DB.Collection("users").CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, 0, err
	}

	return userList, total, nil
}

// EffectiveProfileStatus returns u.ProfileStatus, treating an empty stored
// value (the Go zero value, which should not occur post-migration but is a
// legitimate state for any user created before this field existed and not
// yet backfilled) as ProfileStatusNotSubmitted. Every comparison against
// ProfileStatus should go through this function rather than comparing the
// raw field directly, so callers never have to repeat the empty-string
// fallback themselves.
func EffectiveProfileStatus(u *User) string {
	if u == nil || u.ProfileStatus == "" {
		return ProfileStatusNotSubmitted
	}
	return u.ProfileStatus
}
