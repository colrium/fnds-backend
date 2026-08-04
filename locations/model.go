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
	defer func() { _ = cursor.Close(context.TODO()) }()

	var locations []Location
	if err := cursor.All(context.TODO(), &locations); err != nil {
		return nil, err
	}
	return locations, nil
}

// GetLocationNamesByIDs returns the human-readable names for a list of location IDs.
// Unknown IDs are silently skipped.
func GetLocationNamesByIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	cursor, err := configs.MI.DB.Collection("locations").Find(
		ctx,
		bson.M{"_id": bson.M{"$in": ids}},
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var locs []Location
	if err := cursor.All(ctx, &locs); err != nil {
		return nil, err
	}

	// Preserve the input order.
	nameMap := make(map[string]string, len(locs))
	for _, l := range locs {
		nameMap[l.ID] = l.Name
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := nameMap[id]; ok {
			names = append(names, name)
		}
	}
	return names, nil
}
