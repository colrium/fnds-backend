package institutions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// mongoIndexAlreadyExistsCode is the server error code returned when
// CreateOne is called for an index that already exists with the same spec.
// It's safe to ignore during startup index creation.
const mongoIndexAlreadyExistsCode = 85

// EnsureIndexes creates the search-supporting indexes for institutions:
//   - a text index on name so search can use $text
//   - a plain ascending index on name to accelerate prefix / sort queries
//
// Called once at startup from main.go. Idempotent — an "already exists" error
// from Mongo (code 85) is treated as success so repeated deploys don't crash.
func EnsureIndexes(ctx context.Context) error {
	collection := configs.MI.DB.Collection("institutions")
	models := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "name", Value: "text"}},
			Options: options.Index().SetName("institutions_name_text"),
		},
		{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetName("institutions_name_asc"),
		},
	}
	for _, m := range models {
		if _, err := collection.Indexes().CreateOne(ctx, m); err != nil {
			var cmdErr mongo.CommandError
			if errors.As(err, &cmdErr) && cmdErr.Code == mongoIndexAlreadyExistsCode {
				continue
			}
			return fmt.Errorf("ensure institution indexes: %w", err)
		}
	}
	return nil
}

// DefaultSearchLimit is the fallback page size used when a direct caller
// passes limit <= 0. It matches MaxPageLimit so no caller can bypass the
// HTTP-layer ceiling by going straight to the model.
var DefaultSearchLimit = MaxPageLimit

// MaxSearchOffset caps how deep offset-based pagination can go. SetSkip
// scans and discards `offset` documents server-side, so latency grows
// linearly. This ceiling forces callers past it to narrow their query
// instead. Revisit (or switch to keyset pagination) if the collection
// grows into the tens of thousands.
const MaxSearchOffset int64 = 10_000

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
	defer func() { _ = cursor.Close(ctx) }()

	var institutions []Institution
	if err := cursor.All(ctx, &institutions); err != nil {
		return nil, fmt.Errorf("decode institutions: %w", err)
	}
	return institutions, nil
}

// SearchInstitutions returns institutions matching the query string. When
// query is non-empty it uses a MongoDB $text search against the
// institutions_name_text index and sorts by relevance score. An empty query
// returns institutions sorted alphabetically. Results are paginated via limit
// and offset; if limit <= 0 it falls back to DefaultSearchLimit.
//
// $text tokenizes on word boundaries, so partial words won't match (e.g. "univ"
// won't match "University"). For prefix-style matching on very short inputs
// we fall back to an anchored case-insensitive regex.
func SearchInstitutions(query string, limit int64, offset int64) ([]Institution, error) {
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if offset < 0 {
		offset = 0
	}
	if offset > MaxSearchOffset {
		return nil, fmt.Errorf("offset %d exceeds max %d: narrow the query instead", offset, MaxSearchOffset)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	filter := bson.M{}
	// NOTE: SetSkip is offset-based pagination — Mongo must walk and discard
	// `offset` documents before returning results, so latency degrades linearly
	// with offset. Fine at the current scale (a few hundred institutions), but
	// if this collection grows into the tens of thousands, switch to keyset
	// pagination (e.g. filter { name: { $gt: cursor } } sorted by name).
	opts := options.Find().SetLimit(limit).SetSkip(offset)

	switch {
	case query == "":
		opts.SetSort(bson.D{{Key: "name", Value: 1}})
	case shouldUseTextSearch(query):
		filter["$text"] = bson.M{"$search": query}
		opts.SetProjection(bson.M{"score": bson.M{"$meta": "textScore"}})
		opts.SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}})
	default:
		filter["name"] = bson.M{
			"$regex": primitive.Regex{Pattern: "^" + regexp.QuoteMeta(query), Options: "i"},
		}
		opts.SetSort(bson.D{{Key: "name", Value: 1}})
	}

	cursor, err := configs.MI.DB.Collection("institutions").Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("search institutions (query=%q): %w", query, err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	institutions := []Institution{}
	if err := cursor.All(ctx, &institutions); err != nil {
		return nil, fmt.Errorf("decode institutions (query=%q): %w", query, err)
	}
	return institutions, nil
}

// shouldUseTextSearch reports whether the query contains at least one full
// word (>=3 chars separated by whitespace). Text search does not do partial
// word matching, so short/partial inputs go through the regex prefix path.
func shouldUseTextSearch(query string) bool {
	for _, word := range strings.Fields(query) {
		if len(word) >= 3 {
			return true
		}
	}
	return false
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
