package insurance

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Insurance struct {
	ID              string    `json:"id" bson:"_id"`
	ArtisanID       string    `json:"artisan_id" bson:"artisan_id"`
	Type            string    `json:"type" bson:"type"`
	Option          string    `json:"option" bson:"option"`
	TotalToolValue  float64   `json:"total_tool_value,omitempty" bson:"total_tool_value,omitempty"`
	FamilySize      int       `json:"family_size,omitempty" bson:"family_size,omitempty"`
	NextOfKin       string    `json:"next_of_kin,omitempty" bson:"next_of_kin,omitempty"`
	Sent            bool      `json:"sent" bson:"sent"`
	CreatedAt       time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt       time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateInsurance(i *Insurance) error {
	i.ID = uuid.New().String()
	i.CreatedAt = time.Now()
	i.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("insurances").InsertOne(context.TODO(), i)
	return err
}

func GetInsuranceByID(id string) (*Insurance, error) {
	var i Insurance
	err := configs.MI.DB.Collection("insurances").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&i)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func ListInsurancesByArtisan(artisanID string) ([]Insurance, error) {
	cursor, err := configs.MI.DB.Collection("insurances").Find(context.TODO(), bson.M{"artisan_id": artisanID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var insurances []Insurance
	if err := cursor.All(context.TODO(), &insurances); err != nil {
		return nil, err
	}
	return insurances, nil
}

func ListAllInsurances() ([]Insurance, error) {
	cursor, err := configs.MI.DB.Collection("insurances").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var insurances []Insurance
	if err := cursor.All(context.TODO(), &insurances); err != nil {
		return nil, err
	}
	return insurances, nil
}

func UpdateInsurance(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("insurances").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}
