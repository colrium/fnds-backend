package users

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	EmailNormalized     string     `json:"-" bson:"email_normalized,omitempty"`
	Phone               string     `json:"phone" bson:"phone,omitempty"`
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
	if user.Email != "" {
		user.EmailNormalized = strings.ToLower(strings.TrimSpace(user.Email))
	}
	if user.ProfileStatus == "" {
		user.ProfileStatus = ProfileStatusNotSubmitted
	}
	_, err := configs.MI.DB.Collection("users").InsertOne(context.TODO(), user)
	return err
}

// EnsureIndexes creates the unique constraints for the users collection.
// Phone and email are globally unique: a phone number or email belongs to
// exactly one account regardless of user_type. Email is enforced only when
// present, since it is optional at registration.
//
// The uniq_phone index is rebuilt as a partial unique index so that documents
// without a phone field are excluded from the uniqueness constraint. This
// permits multiple phone-less employer accounts created through Stytch.
// The migration is a 4-step ordered operation executed in this function.
// indexMigrationLockTTL bounds how long a migration lock is held before it is
// considered abandoned (e.g. the holder crashed) and can be reacquired.
const indexMigrationLockTTL = 5 * time.Minute

// acquireIndexMigrationLock takes an exclusive, TTL-bounded lock on the given
// name using an upsert-based findAndModify against a dedicated migrations
// collection, so concurrent instances racing through EnsureIndexes at startup
// serialize instead of both mutating indexes at once. Returns locked=false
// (not an error) when another instance already holds the lock.
func acquireIndexMigrationLock(ctx context.Context, name string) (release func(context.Context), locked bool, err error) {
	coll := configs.MI.DB.Collection("migration_locks")
	now := time.Now()

	res := coll.FindOneAndUpdate(ctx,
		bson.M{
			"_id":        name,
			"expires_at": bson.M{"$lt": now},
		},
		bson.M{"$set": bson.M{"expires_at": now.Add(indexMigrationLockTTL), "locked_at": now}},
		options.FindOneAndUpdate().SetUpsert(false),
	)
	if res.Err() != nil && res.Err() != mongo.ErrNoDocuments {
		return nil, false, res.Err()
	}
	if res.Err() == nil {
		// Reacquired an expired lock.
		return func(releaseCtx context.Context) {
			_, _ = coll.DeleteOne(releaseCtx, bson.M{"_id": name})
		}, true, nil
	}

	// No expired lock to take over — try to create a fresh one.
	_, insertErr := coll.InsertOne(ctx, bson.M{
		"_id":        name,
		"expires_at": now.Add(indexMigrationLockTTL),
		"locked_at":  now,
	})
	if insertErr != nil {
		if mongo.IsDuplicateKeyError(insertErr) {
			return nil, false, nil
		}
		return nil, false, insertErr
	}
	return func(releaseCtx context.Context) {
		_, _ = coll.DeleteOne(releaseCtx, bson.M{"_id": name})
	}, true, nil
}

func EnsureIndexes(ctx context.Context) error {
	// Acquire a distributed lock before touching indexes. Without it, every
	// instance in a multi-replica deployment races through the drop+create
	// sequence concurrently: between one instance's DropOne and CreateOne
	// there is no unique constraint on phone at all, so concurrent signups
	// can insert duplicates that make the next instance's CreateOne fail.
	release, locked, err := acquireIndexMigrationLock(ctx, "users_phone_index_migration")
	if err != nil {
		return fmt.Errorf("users.EnsureIndexes: failed to acquire migration lock: %w", err)
	}
	if !locked {
		slog.Info("users.EnsureIndexes: migration lock held by another instance, skipping")
		return nil
	}
	defer release(ctx)

	coll := configs.MI.DB.Collection("users")

	// ─── Step 1: Pre-check for duplicate phone values ───────────────────
	// Aggregate users grouped by phone where phone is a non-empty string.
	// If any group has count > 1, startup must fail — we cannot safely drop
	// a unique index over data that already has duplicates.
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"phone": bson.M{"$type": "string", "$gt": ""}}}},
		{{Key: "$group", Value: bson.M{"_id": "$phone", "count": bson.M{"$sum": 1}}}},
		{{Key: "$match", Value: bson.M{"count": bson.M{"$gt": 1}}}},
		{{Key: "$count", Value: "duplicate_count"}},
	}
	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return fmt.Errorf("users.EnsureIndexes: phone duplicate pre-check aggregate failed: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return fmt.Errorf("users.EnsureIndexes: phone duplicate pre-check cursor read failed: %w", err)
	}
	if len(results) > 0 {
		dupCount, _ := results[0]["duplicate_count"].(int32)
		slog.Error("users.EnsureIndexes: cannot rebuild uniq_phone — duplicate phone values exist",
			"duplicate_count", dupCount)
		return fmt.Errorf("users.EnsureIndexes: %d phone values have duplicates; resolve before restarting", dupCount)
	}

	// ─── Step 2: Normalize empty phone values ───────────────────────────
	// Unset phone on any document where phone is empty OR whitespace-only, so
	// the partial index filter (which matches any non-empty string, including
	// "   ") correctly excludes them. Without the whitespace match, two
	// whitespace-only phones would both pass the pre-check and the partial
	// index build in Step 3/4 would fail with a duplicate key error.
	_, err = coll.UpdateMany(ctx,
		bson.M{"phone": bson.M{"$regex": "^\\s*$"}},
		bson.M{"$unset": bson.M{"phone": ""}},
	)
	if err != nil {
		return fmt.Errorf("users.EnsureIndexes: failed to unset empty phone values: %w", err)
	}

	// ─── Step 3 & 4: Rebuild the three partial unique indexes (idempotent) ──
	// Read existing indexes and only drop+recreate one when its current
	// definition differs from the target, so a restart is a no-op. Blasting
	// CreateMany unconditionally (as uniq_email/uniq_slug_public previously
	// did) fails the entire startup with IndexOptionsConflict the moment an
	// existing index's options differ even slightly from the target.
	partialIndexes := []struct {
		name string
		keys bson.D
		pfe  bson.M
	}{
		{
			name: "uniq_phone",
			keys: bson.D{{Key: "phone", Value: 1}},
			pfe:  bson.M{"phone": bson.M{"$type": "string", "$gt": ""}},
		},
		{
			name: "uniq_email",
			keys: bson.D{{Key: "email", Value: 1}},
			pfe:  bson.M{"email": bson.M{"$type": "string", "$gt": ""}},
		},
		{
			name: "uniq_slug_public",
			keys: bson.D{{Key: "slug", Value: 1}},
			pfe: bson.M{
				"slug":              bson.M{"$type": "string", "$gt": ""},
				"is_profile_public": true,
			},
		},
	}

	for _, idx := range partialIndexes {
		needsRebuild, err := partialIndexNeedsRebuild(ctx, coll, idx.name, idx.pfe)
		if err != nil {
			return fmt.Errorf("users.EnsureIndexes: failed to inspect existing index %s: %w", idx.name, err)
		}
		if !needsRebuild {
			continue
		}
		// Drop the old index (may fail if it doesn't exist; that's acceptable).
		_, _ = coll.Indexes().DropOne(ctx, idx.name)
		if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: idx.keys,
			Options: options.Index().
				SetName(idx.name).
				SetUnique(true).
				SetPartialFilterExpression(idx.pfe),
		}); err != nil {
			return fmt.Errorf("users.EnsureIndexes: failed to create partial index %s: %w", idx.name, err)
		}
	}

	// ─── Remaining index (unchanged, not a unique partial index) ────────
	_, err = coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "email_normalized", Value: 1}},
			Options: options.Index().SetName("users_email_ci"),
		},
	})
	return err
}

// partialIndexNeedsRebuild inspects the existing indexes on the collection and
// returns true when the named index either does not exist or its
// partialFilterExpression does not match the target, meaning it must be
// dropped and recreated. The PFE comparison uses a canonical JSON encoding
// (sorted keys) rather than a raw BSON byte comparison, because MongoDB can
// store a semantically identical filter with different field ordering.
func partialIndexNeedsRebuild(ctx context.Context, coll *mongo.Collection, indexName string, targetPFE bson.M) (bool, error) {
	cursor, err := coll.Indexes().List(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	for cursor.Next(ctx) {
		var idx bson.M
		if err := cursor.Decode(&idx); err != nil {
			return false, err
		}
		name, _ := idx["name"].(string)
		if name != indexName {
			continue
		}
		// Index exists — check whether it already has the correct partial filter.
		pfe, hasPFE := idx["partialFilterExpression"]
		if !hasPFE {
			// Non-partial index exists — needs rebuild.
			return true, nil
		}
		pfeMap, ok := pfe.(bson.M)
		if !ok {
			return true, nil
		}
		if !canonicalJSONEqual(pfeMap, targetPFE) {
			return true, nil
		}
		// Index exists with correct partial filter — no rebuild needed.
		return false, nil
	}
	// Index not found at all — needs creation.
	return true, nil
}

// canonicalJSONEqual compares two BSON documents structurally via a
// marshal-to-canonical-JSON round trip, which sorts object keys and
// normalizes numeric representations — unlike a raw BSON byte comparison,
// this treats field-order differences as equal rather than as a mismatch.
func canonicalJSONEqual(a, b bson.M) bool {
	aJSON, errA := bson.MarshalExtJSON(a, true, false)
	bJSON, errB := bson.MarshalExtJSON(b, true, false)
	if errA != nil || errB != nil {
		return false
	}
	var aCanon, bCanon any
	if json.Unmarshal(aJSON, &aCanon) != nil || json.Unmarshal(bJSON, &bCanon) != nil {
		return false
	}
	aSorted, errA := json.Marshal(aCanon)
	bSorted, errB := json.Marshal(bCanon)
	if errA != nil || errB != nil {
		return false
	}
	return string(aSorted) == string(bSorted)
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

// GetUsersByNormalizedEmail returns ALL users whose email_normalized field
// matches the given email after normalization. This is needed because the
// existing GetUserByEmail does an exact match and can't find case-variant
// duplicates. (Req 4.1, 4.8, 21.7)
func GetUsersByNormalizedEmail(ctx context.Context, normalizedEmail string) ([]User, error) {
	coll := configs.MI.DB.Collection("users")
	cursor, err := coll.Find(ctx, bson.M{"email_normalized": normalizedEmail})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()
	var users []User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, err
	}
	return users, nil
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
