package documenttypes

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type DocumentType struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateDocumentType(dt *DocumentType) error {
	dt.ID = uuid.New().String()
	dt.CreatedAt = time.Now()
	dt.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("document_types").InsertOne(context.TODO(), dt)
	return err
}

func GetDocumentTypeByID(id string) (*DocumentType, error) {
	var dt DocumentType
	err := configs.MI.DB.Collection("document_types").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&dt)
	if err != nil {
		return nil, err
	}
	return &dt, nil
}

func ListDocumentTypes() ([]DocumentType, error) {
	cursor, err := configs.MI.DB.Collection("document_types").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var types []DocumentType
	if err := cursor.All(context.TODO(), &types); err != nil {
		return nil, err
	}
	return types, nil
}

func UpdateDocumentType(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("document_types").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func DeleteDocumentType(id string) error {
	_, err := configs.MI.DB.Collection("document_types").DeleteOne(context.TODO(), bson.M{"_id": id})
	return err
}
