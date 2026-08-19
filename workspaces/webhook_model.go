package workspaces

import (
	"context"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ProcessedWebhookEvent records a Stytch webhook event that has been consumed,
// enabling idempotent redelivery suppression. The document expires 30 days
// after processing via a TTL index on expires_at.
// Requirements: 19.4.
type ProcessedWebhookEvent struct {
	ID             string    `bson:"_id"` // svix-id
	EventType      string    `bson:"event_type"`
	TargetKind     string    `bson:"target_kind"`
	TargetID       string    `bson:"target_id"`
	EventTimestamp time.Time `bson:"event_timestamp"`
	ProcessedAt    time.Time `bson:"processed_at"`
	ExpiresAt      time.Time `bson:"expires_at"` // processed_at + 30d
}

// EnsureWebhookIndexes creates the indexes for the processed_webhook_events
// collection. Called once at startup from main.go. Idempotent.
// Requirements: 19.4.
func EnsureWebhookIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("processed_webhook_events")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// webhook_events_ttl — TTL index that expires documents when
			// expires_at is reached (expireAfterSeconds: 0 means "at the value").
			Keys: bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().
				SetName("webhook_events_ttl").
				SetExpireAfterSeconds(0),
		},
	})
	return err
}
