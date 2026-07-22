package notifications

import (
	"context"
	"fmt"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	TypeNewApplication    = "new_application"
	TypeApplicationStatus = "application_status"
	TypeSkillMatch        = "skill_match"
)

type DeliveryChannel string

const (
	DeliveryChannelEmail DeliveryChannel = "email"
	DeliveryChannelSMS   DeliveryChannel = "sms"
)

type Delivery struct {
	Channel       DeliveryChannel `bson:"channel"`
	To            string          `bson:"to"`
	Subject       string          `bson:"subject,omitempty"`
	Body          string          `bson:"body"`
	Status        string          `bson:"status"`
	Attempts      int             `bson:"attempts"`
	NextAttemptAt time.Time       `bson:"next_attempt_at"`
	LockedUntil   *time.Time      `bson:"locked_until,omitempty"`
	DeliveredAt   *time.Time      `bson:"delivered_at,omitempty"`
}

type Notification struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	Type      string    `json:"type" bson:"type"`
	Title     string    `json:"title" bson:"title"`
	Message   string    `json:"message" bson:"message"`
	ActionURL string    `json:"action_url" bson:"action_url"`
	Read      bool      `json:"read" bson:"read"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	Delivery  *Delivery `json:"-" bson:"delivery,omitempty"`
}

type Feed struct {
	Notifications []Notification `json:"notifications"`
	UnreadCount   int64          `json:"unread_count"`
}

type MongoStore struct {
	collection *mongo.Collection
}

func NewMongoStore() *MongoStore {
	return &MongoStore{collection: configs.MI.DB.Collection("notifications")}
}

func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("notifications").Indexes().CreateMany(
		ctx,
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "user_id", Value: 1},
					{Key: "created_at", Value: -1},
				},
				Options: options.Index().SetName("notifications_user_created"),
			},
			{
				Keys: bson.D{
					{Key: "user_id", Value: 1},
					{Key: "read", Value: 1},
				},
				Options: options.Index().SetName("notifications_user_unread"),
			},
			{
				Keys: bson.D{
					{Key: "delivery.status", Value: 1},
					{Key: "delivery.next_attempt_at", Value: 1},
				},
				Options: options.Index().SetName("notifications_pending_delivery"),
			},
		},
	)
	return err
}

func (s *MongoStore) Create(ctx context.Context, notification Notification) error {
	prepareNotification(&notification)
	if _, err := s.collection.InsertOne(ctx, notification); err != nil {
		return fmt.Errorf("creating notification: %w", err)
	}
	return nil
}

func (s *MongoStore) CreateMany(ctx context.Context, notifications []Notification) error {
	if len(notifications) == 0 {
		return nil
	}
	documents := make([]interface{}, 0, len(notifications))
	for i := range notifications {
		prepareNotification(&notifications[i])
		documents = append(documents, notifications[i])
	}
	if _, err := s.collection.InsertMany(ctx, documents); err != nil {
		return fmt.Errorf("creating notifications: %w", err)
	}
	return nil
}

func (s *MongoStore) List(ctx context.Context, userID string, limit int64) (Feed, error) {
	findOptions := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(limit)
	cursor, err := s.collection.Find(ctx, bson.M{"user_id": userID}, findOptions)
	if err != nil {
		return Feed{}, fmt.Errorf("listing notifications: %w", err)
	}
	defer cursor.Close(ctx)

	notifications := []Notification{}
	if err := cursor.All(ctx, &notifications); err != nil {
		return Feed{}, fmt.Errorf("decoding notifications: %w", err)
	}
	unreadCount, err := s.collection.CountDocuments(
		ctx,
		bson.M{"user_id": userID, "read": false},
	)
	if err != nil {
		return Feed{}, fmt.Errorf("counting unread notifications: %w", err)
	}
	return Feed{Notifications: notifications, UnreadCount: unreadCount}, nil
}

func (s *MongoStore) MarkRead(ctx context.Context, userID, notificationID string) (bool, error) {
	result, err := s.collection.UpdateOne(
		ctx,
		bson.M{"_id": notificationID, "user_id": userID},
		bson.M{"$set": bson.M{"read": true}},
	)
	if err != nil {
		return false, fmt.Errorf("marking notification read: %w", err)
	}
	return result.MatchedCount > 0, nil
}

func (s *MongoStore) MarkAllRead(ctx context.Context, userID string) error {
	if _, err := s.collection.UpdateMany(
		ctx,
		bson.M{"user_id": userID, "read": false},
		bson.M{"$set": bson.M{"read": true}},
	); err != nil {
		return fmt.Errorf("marking all notifications read: %w", err)
	}
	return nil
}

func (s *MongoStore) ClaimDelivery(ctx context.Context) (*Notification, error) {
	now := time.Now().UTC()
	lockExpiry := now.Add(time.Minute)
	var notification Notification
	err := s.collection.FindOneAndUpdate(
		ctx,
		bson.M{"$or": bson.A{
			bson.M{
				"delivery.status":          "pending",
				"delivery.next_attempt_at": bson.M{"$lte": now},
			},
			bson.M{
				"delivery.status":       "processing",
				"delivery.locked_until": bson.M{"$lte": now},
			},
		}},
		bson.M{
			"$set": bson.M{
				"delivery.status":       "processing",
				"delivery.locked_until": lockExpiry,
			},
			"$inc": bson.M{"delivery.attempts": 1},
		},
		options.FindOneAndUpdate().
			SetSort(bson.D{{Key: "delivery.next_attempt_at", Value: 1}}).
			SetReturnDocument(options.After),
	).Decode(&notification)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claiming notification delivery: %w", err)
	}
	return &notification, nil
}

func (s *MongoStore) MarkDeliverySent(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.collection.UpdateOne(
		ctx,
		bson.M{"_id": id, "delivery.status": "processing"},
		bson.M{
			"$set": bson.M{
				"delivery.status":       "sent",
				"delivery.delivered_at": now,
			},
			"$unset": bson.M{"delivery.locked_until": ""},
		},
	)
	if err != nil {
		return fmt.Errorf("marking notification delivery sent: %w", err)
	}
	return nil
}

func (s *MongoStore) MarkDeliveryFailed(ctx context.Context, id string, _ error) error {
	_, err := s.collection.UpdateOne(
		ctx,
		bson.M{"_id": id, "delivery.status": "processing"},
		bson.M{
			"$set": bson.M{
				"delivery.status":          "pending",
				"delivery.next_attempt_at": time.Now().UTC().Add(time.Minute),
			},
			"$unset": bson.M{"delivery.locked_until": ""},
		},
	)
	if err != nil {
		return fmt.Errorf("scheduling notification delivery retry: %w", err)
	}
	return nil
}

func prepareNotification(notification *Notification) {
	notification.ID = uuid.New().String()
	notification.Read = false
	notification.CreatedAt = time.Now().UTC()
	if notification.Delivery != nil {
		notification.Delivery.Status = "pending"
		notification.Delivery.Attempts = 0
		notification.Delivery.NextAttemptAt = notification.CreatedAt
	}
}
