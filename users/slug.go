package users

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

var (
	nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]`)
	multipleHyphens   = regexp.MustCompile(`-{2,}`)
)

// GenerateSlug produces a keyword-rich, URL-safe slug from the artisan's name,
// primary skill, and city. Example: "dennis-mwangi-plumber-nairobi".
func GenerateSlug(firstName, lastName, skill, city string) string {
	parts := []string{firstName, lastName, skill, city}
	var nonEmpty []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	raw := strings.Join(nonEmpty, "-")
	raw = strings.ToLower(raw)
	raw = strings.ReplaceAll(raw, " ", "-")
	raw = nonAlphanumHyphen.ReplaceAllString(raw, "")
	raw = multipleHyphens.ReplaceAllString(raw, "-")
	raw = strings.Trim(raw, "-")
	return raw
}

// PublishWithUniqueSlug atomically publishes a user's profile with a unique
// slug. MongoDB's partial unique index is the source of truth; duplicate-key
// conflicts are retried with numeric suffixes (-2, -3, ...).
func PublishWithUniqueSlug(ctx context.Context, userID, base string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user ID cannot be empty")
	}
	if base == "" {
		return "", fmt.Errorf("cannot publish profile with empty slug")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("publishing profile: %w", err)
	}

	// Once publication begins, let the atomic MongoDB update finish even if the
	// request disconnects. Keep one short deadline across all uniqueness retries.
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	collection := configs.MI.DB.Collection("users")
	for attempt := 1; attempt <= 100; attempt++ {
		candidate := base
		if attempt > 1 {
			candidate = fmt.Sprintf("%s-%d", base, attempt)
		}

		result, err := collection.UpdateOne(
			publishCtx,
			bson.M{"_id": userID},
			bson.M{"$set": bson.M{
				"slug":              candidate,
				"is_profile_public": true,
				"updated_at":        time.Now(),
			}},
		)
		if err == nil {
			if result.MatchedCount == 0 {
				return "", fmt.Errorf("user not found when publishing profile: %w", mongo.ErrNoDocuments)
			}
			return candidate, nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return "", fmt.Errorf("publishing profile with slug %q: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("could not reserve unique slug after 100 attempts")
}
