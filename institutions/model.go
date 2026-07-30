package institutions

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureIndexes creates the search-supporting indexes for institutions:
//   - a text index on name so future search can switch to $text
//   - a plain ascending index on name to accelerate prefix / sort queries
//
// Called once at startup from main.go.
func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("institutions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "name", Value: "text"}},
			Options: options.Index().SetName("institutions_name_text"),
		},
		{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetName("institutions_name_asc"),
		},
	})
	if err != nil {
		return fmt.Errorf("ensure institution indexes: %w", err)
	}
	return nil
}

// DefaultSearchLimit caps the number of results returned from a search or
// list operation to protect the pod from OOM on large collections.
const DefaultSearchLimit int64 = 100

// dbTimeout is the default timeout applied to institution DB operations.
const dbTimeout = 5 * time.Second

type Institution struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	Type      string    `json:"type" bson:"type"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func GetAllInstitutions() ([]Institution, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := configs.MI.DB.Collection("institutions").Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, fmt.Errorf("list institutions: %w", err)
	}
	defer cursor.Close(ctx)

	var institutions []Institution
	if err := cursor.All(ctx, &institutions); err != nil {
		return nil, fmt.Errorf("decode institutions: %w", err)
	}
	return institutions, nil
}

// SearchInstitutions returns institutions whose names contain the query string,
// matched case-insensitively, sorted alphabetically by name. An empty query
// returns institutions sorted alphabetically. Results are paginated via limit
// and offset; if limit <= 0 it falls back to DefaultSearchLimit.
func SearchInstitutions(query string, limit int64, offset int64) ([]Institution, error) {
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if offset < 0 {
		offset = 0
	}

	filter := bson.M{}
	if query != "" {
		filter["name"] = bson.M{
			"$regex": primitive.Regex{Pattern: regexp.QuoteMeta(query), Options: "i"},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	opts := options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}}).
		SetLimit(limit).
		SetSkip(offset)
	cursor, err := configs.MI.DB.Collection("institutions").Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("search institutions (query=%q): %w", query, err)
	}
	defer cursor.Close(ctx)

	institutions := []Institution{}
	if err := cursor.All(ctx, &institutions); err != nil {
		return nil, fmt.Errorf("decode institutions (query=%q): %w", query, err)
	}
	return institutions, nil
}

func GetInstitutionByID(id string) (*Institution, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	var institution Institution
	err := configs.MI.DB.Collection("institutions").FindOne(ctx, bson.M{"_id": id}).Decode(&institution)
	if err != nil {
		return nil, fmt.Errorf("get institution %s: %w", id, err)
	}
	return &institution, nil
}

func CreateInstitution(inst *Institution) error {
	inst.ID = uuid.New().String()
	inst.CreatedAt = time.Now()
	inst.UpdatedAt = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	if _, err := configs.MI.DB.Collection("institutions").InsertOne(ctx, inst); err != nil {
		return fmt.Errorf("create institution: %w", err)
	}
	return nil
}

func UpdateInstitution(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	if _, err := configs.MI.DB.Collection("institutions").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": updates}); err != nil {
		return fmt.Errorf("update institution %s: %w", id, err)
	}
	return nil
}
