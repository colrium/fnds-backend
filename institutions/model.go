package institutions

import (
	"context"
	"regexp"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
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

// SearchInstitutions returns institutions whose names contain the query string,
// matched case-insensitively, sorted alphabetically by name. An empty query
// returns all institutions sorted alphabetically.
func SearchInstitutions(query string) ([]Institution, error) {
	filter := bson.M{}
	if query != "" {
		filter["name"] = bson.M{
			"$regex": primitive.Regex{Pattern: regexp.QuoteMeta(query), Options: "i"},
		}
	}

	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := configs.MI.DB.Collection("institutions").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	institutions := []Institution{}
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
