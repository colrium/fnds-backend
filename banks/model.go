package banks

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Bank struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	Code      string    `json:"code" bson:"code"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

func GetAllBanks() ([]Bank, error) {
	cursor, err := configs.MI.DB.Collection("banks").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	var banks []Bank
	if err := cursor.All(context.TODO(), &banks); err != nil {
		return nil, err
	}
	return banks, nil
}

func CreateBank(name, code string) error {
	_, err := configs.MI.DB.Collection("banks").InsertOne(context.TODO(), Bank{
		ID:        uuid.New().String(),
		Name:      name,
		Code:      code,
		CreatedAt: time.Now(),
	})
	return err
}
