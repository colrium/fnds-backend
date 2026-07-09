package reviews

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Review struct {
	ID           string    `json:"id" bson:"_id"`
	JobRequestID string    `json:"job_request_id" bson:"job_request_id"`
	ArtisanID    string    `json:"artisan_id" bson:"artisan_id"`
	CustomerID   string    `json:"customer_id" bson:"customer_id"`
	Rating       int       `json:"rating" bson:"rating"`
	Comment      string    `json:"comment" bson:"comment"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateReview(r *Review) error {
	r.ID = uuid.New().String()
	r.CreatedAt = time.Now()
	r.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("reviews").InsertOne(context.TODO(), r)
	return err
}

func ListReviews(filter bson.M) ([]Review, error) {
	cursor, err := configs.MI.DB.Collection("reviews").Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var reviews []Review
	if err := cursor.All(context.TODO(), &reviews); err != nil {
		return nil, err
	}
	return reviews, nil
}
