package users

import (
	"context"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestGetUserBySlugUsesExactPublicArtisanFilter(t *testing.T) {
	mt := useMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{
			{Key: "_id", Value: "user-id"},
			{Key: "slug", Value: "jane-doe-plumber"},
			{Key: "user_type", Value: "artisan"},
			{Key: "is_profile_public", Value: true},
		},
	))

	user, err := GetUserBySlug(context.Background(), "jane-doe-plumber")
	if err != nil {
		t.Fatalf("GetUserBySlug() error = %v", err)
	}
	if user.ID != "user-id" || user.Slug != "jane-doe-plumber" {
		t.Errorf("GetUserBySlug() user = %#v", user)
	}
	assertStartedFilter(t, mt, bson.M{
		"slug":              "jane-doe-plumber",
		"is_profile_public": true,
		"user_type":         "artisan",
	})
}

func TestGetPublicUserByFundisIDUsesExactPublicArtisanFilter(t *testing.T) {
	mt := useMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{
			{Key: "_id", Value: "user-id"},
			{Key: "fundis_id", Value: "FUN-123"},
			{Key: "slug", Value: "jane-doe-plumber"},
		},
	))

	user, err := GetPublicUserByFundisID(context.Background(), "FUN-123")
	if err != nil {
		t.Fatalf("GetPublicUserByFundisID() error = %v", err)
	}
	if user.FundisID != "FUN-123" || user.Slug != "jane-doe-plumber" {
		t.Errorf("GetPublicUserByFundisID() user = %#v", user)
	}
	assertStartedFilter(t, mt, bson.M{
		"fundis_id":         "FUN-123",
		"is_profile_public": true,
		"user_type":         "artisan",
	})
}

func TestEnsureIndexesIncludesUniquePublicSlugIndex(t *testing.T) {
	mt := useMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "numIndexesBefore", Value: 1},
		bson.E{Key: "numIndexesAfter", Value: 4},
	))

	if err := EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes() error = %v", err)
	}

	event := mt.GetStartedEvent()
	if event == nil || event.CommandName != "createIndexes" {
		t.Fatalf("started event = %#v, want createIndexes", event)
	}
	var command struct {
		Indexes []struct {
			Key                     bson.D `bson:"key"`
			Name                    string `bson:"name"`
			Unique                  bool   `bson:"unique"`
			PartialFilterExpression bson.M `bson:"partialFilterExpression"`
		} `bson:"indexes"`
	}
	if err := bson.Unmarshal(event.Command, &command); err != nil {
		t.Fatalf("decode createIndexes command: %v", err)
	}
	if len(command.Indexes) != 3 {
		t.Fatalf("index count = %d, want 3", len(command.Indexes))
	}

	slugIndex := command.Indexes[2]
	if slugIndex.Name != "uniq_slug_public" {
		t.Errorf("slug index name = %q, want uniq_slug_public", slugIndex.Name)
	}
	if !slugIndex.Unique {
		t.Error("slug index is not unique")
	}
	if !reflect.DeepEqual(slugIndex.Key, bson.D{{Key: "slug", Value: int32(1)}}) {
		t.Errorf("slug index key = %#v", slugIndex.Key)
	}
	wantPartial := bson.M{
		"slug":              bson.M{"$type": "string", "$gt": ""},
		"is_profile_public": true,
	}
	if !reflect.DeepEqual(slugIndex.PartialFilterExpression, wantPartial) {
		t.Errorf("slug partial filter = %#v, want %#v", slugIndex.PartialFilterExpression, wantPartial)
	}
}

func assertStartedFilter(t *testing.T, mt *mtest.T, want bson.M) {
	t.Helper()

	event := mt.GetStartedEvent()
	if event == nil || event.CommandName != "find" {
		t.Fatalf("started event = %#v, want find", event)
	}
	var got bson.M
	if err := bson.Unmarshal(event.Command.Lookup("filter").Document(), &got); err != nil {
		t.Fatalf("decode find filter: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("find filter = %#v, want %#v", got, want)
	}
}
