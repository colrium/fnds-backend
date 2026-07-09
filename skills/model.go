package skills

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Skill struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

func CreateSkill(name string) error {
	_, err := configs.MI.DB.Collection("skills").InsertOne(context.TODO(), Skill{
		ID:        uuid.New().String(),
		Name:      name,
		CreatedAt: time.Now(),
	})
	return err
}

func GetAllSkills() ([]Skill, error) {
	cursor, err := configs.MI.DB.Collection("skills").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	var skills []Skill
	if err := cursor.All(context.TODO(), &skills); err != nil {
		return nil, err
	}
	return skills, nil
}
