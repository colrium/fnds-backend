package locations

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Location struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

func CreateLocation(name string) error {
	_, err := configs.MI.DB.Collection("locations").InsertOne(context.TODO(), Location{
		ID:        uuid.New().String(),
		Name:      name,
		CreatedAt: time.Now(),
	})
	return err
}

func GetAllLocations() ([]Location, error) {
	cursor, err := configs.MI.DB.Collection("locations").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	var locations []Location
	if err := cursor.All(context.TODO(), &locations); err != nil {
		return nil, err
	}
	return locations, nil
}
