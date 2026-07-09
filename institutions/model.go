package institutions

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Institution struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	Type      string    `json:"type" bson:"type"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func GetAllInstitutions() ([]Institution, error) {
	cursor, err := configs.MI.DB.Collection("institutions").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	var institutions []Institution
	if err := cursor.All(context.TODO(), &institutions); err != nil {
		return nil, err
	}
	return institutions, nil
}

func GetInstitutionByID(id string) (*Institution, error) {
	var institution Institution
	err := configs.MI.DB.Collection("institutions").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&institution)
	if err != nil {
		return nil, err
	}
	return &institution, nil
}

func CreateInstitution(inst *Institution) error {
	inst.ID = uuid.New().String()
	inst.CreatedAt = time.Now()
	inst.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("institutions").InsertOne(context.TODO(), inst)
	return err
}

func UpdateInstitution(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("institutions").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}
