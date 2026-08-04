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

// GetSkillByID retrieves a single skill by its ID.
func GetSkillByID(ctx context.Context, id string) (*Skill, error) {
	var skill Skill
	err := configs.MI.DB.Collection("skills").FindOne(ctx, bson.M{"_id": id}).Decode(&skill)
	if err != nil {
		return nil, err
	}
	return &skill, nil
}

// GetSkillNamesByIDs returns the human-readable names for a list of skill IDs.
// Unknown IDs are silently skipped.
func GetSkillNamesByIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	cursor, err := configs.MI.DB.Collection("skills").Find(
		ctx,
		bson.M{"_id": bson.M{"$in": ids}},
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var skills []Skill
	if err := cursor.All(ctx, &skills); err != nil {
		return nil, err
	}

	// Preserve the input order.
	nameMap := make(map[string]string, len(skills))
	for _, s := range skills {
		nameMap[s.ID] = s.Name
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := nameMap[id]; ok {
			names = append(names, name)
		}
	}
	return names, nil
}
