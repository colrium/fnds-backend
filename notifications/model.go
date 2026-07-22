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

type Notification struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	Type      string    `json:"type" bson:"type"`
	Title     string    `json:"title" bson:"title"`
	Message   string    `json:"message" bson:"message"`
	ActionURL string    `json:"action_url" bson:"action_url"`
	Read      bool      `json:"read" bson:"read"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
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

func prepareNotification(notification *Notification) {
	notification.ID = uuid.New().String()
	notification.Read = false
	notification.CreatedAt = time.Now().UTC()
}
