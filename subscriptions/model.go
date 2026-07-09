package subscriptions

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type SubscriptionPlan struct {
	ID          string    `json:"id" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Description string    `json:"description" bson:"description"`
	Price       float64   `json:"price" bson:"price"`
	Duration    int       `json:"duration" bson:"duration"`
	Features    []string  `json:"features" bson:"features"`
	IsDefault   bool      `json:"is_default" bson:"is_default"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type UserSubscription struct {
	ID                 string    `json:"id" bson:"_id"`
	UserID             string    `json:"user_id" bson:"user_id"`
	SubscriptionPlanID string    `json:"subscription_plan_id" bson:"subscription_plan_id"`
	StartDate          time.Time `json:"start_date" bson:"start_date"`
	EndDate            *time.Time `json:"end_date,omitempty" bson:"end_date,omitempty"`
	IsActive           bool      `json:"is_active" bson:"is_active"`
	CreatedAt          time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time `json:"updated_at" bson:"updated_at"`
}

func GetAllPlans() ([]SubscriptionPlan, error) {
	cursor, err := configs.MI.DB.Collection("subscription_plans").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var plans []SubscriptionPlan
	if err := cursor.All(context.TODO(), &plans); err != nil {
		return nil, err
	}
	return plans, nil
}

func GetUserSubscription(userID string) (*UserSubscription, error) {
	var sub UserSubscription
	err := configs.MI.DB.Collection("user_subscriptions").FindOne(context.TODO(), bson.M{"user_id": userID, "is_active": true}).Decode(&sub)
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func CreateUserSubscription(sub *UserSubscription) error {
	sub.ID = uuid.New().String()
	sub.CreatedAt = time.Now()
	sub.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("user_subscriptions").InsertOne(context.TODO(), sub)
	return err
}

func UpdateUserSubscription(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("user_subscriptions").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func GetDefaultPlan() (*SubscriptionPlan, error) {
	var plan SubscriptionPlan
	err := configs.MI.DB.Collection("subscription_plans").FindOne(context.TODO(), bson.M{"is_default": true}).Decode(&plan)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}
