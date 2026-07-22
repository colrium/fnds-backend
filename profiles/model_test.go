package profiles

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type recordingProfileCollection struct {
	options []*options.UpdateOptions
	err     error
}

func (c *recordingProfileCollection) UpdateOne(
	_ context.Context,
	_, _ interface{},
	opts ...*options.UpdateOptions,
) (*mongo.UpdateResult, error) {
	c.options = opts
	return &mongo.UpdateResult{}, c.err
}

func TestUpsertProfile_PropagatesWriteFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	collection := &recordingProfileCollection{err: writeErr}

	err := upsertProfile(
		context.Background(),
		collection,
		"worker-123",
		bson.M{"address": "Kigali"},
	)
	if !errors.Is(err, writeErr) {
		t.Fatalf("upsertProfile() error = %v, want wrapped write error", err)
	}
}

func TestUpsertProfile_CreatesMissingProfile(t *testing.T) {
	collection := &recordingProfileCollection{}

	err := upsertProfile(
		context.Background(),
		collection,
		"worker-123",
		bson.M{"address": "Kigali"},
	)
	if err != nil {
		t.Fatalf("upsertProfile() error = %v", err)
	}

	if len(collection.options) != 1 ||
		collection.options[0].Upsert == nil ||
		!*collection.options[0].Upsert {
		t.Fatal("upsertProfile() did not enable upsert for a missing profile")
	}
}
