package locations

import (
	"context"
	"strings"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestGetLocationNamesByIDs(t *testing.T) {
	t.Run("empty IDs bypass Mongo and return a non-nil slice", func(t *testing.T) {
		names, err := GetLocationNamesByIDs(context.Background(), nil)
		if err != nil {
			t.Fatalf("GetLocationNamesByIDs() error = %v", err)
		}
		if names == nil || len(names) != 0 {
			t.Errorf("GetLocationNamesByIDs() = %#v, want empty non-nil slice", names)
		}
	})

	t.Run("preserves input order and skips unknown IDs", func(t *testing.T) {
		mt := useLocationsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			"test.locations",
			mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "location-2"}, {Key: "name", Value: "Kiambu"}},
			bson.D{{Key: "_id", Value: "location-1"}, {Key: "name", Value: "Nairobi"}},
		))

		names, err := GetLocationNamesByIDs(
			context.Background(),
			[]string{"location-1", "unknown", "location-2", "location-1"},
		)
		if err != nil {
			t.Fatalf("GetLocationNamesByIDs() error = %v", err)
		}
		want := []string{"Nairobi", "Kiambu", "Nairobi"}
		if strings.Join(names, "|") != strings.Join(want, "|") {
			t.Errorf("GetLocationNamesByIDs() = %#v, want %#v", names, want)
		}
	})

	t.Run("returns cursor decode errors", func(t *testing.T) {
		mt := useLocationsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			"test.locations",
			mtest.FirstBatch,
			bson.D{{Key: "_id", Value: "location-1"}, {Key: "name", Value: int32(42)}},
		))

		names, err := GetLocationNamesByIDs(context.Background(), []string{"location-1"})
		if err == nil {
			t.Fatal("GetLocationNamesByIDs() error = nil, want cursor decode error")
		}
		if !strings.Contains(err.Error(), "cannot decode") {
			t.Errorf("GetLocationNamesByIDs() error = %v, want cursor decode error", err)
		}
		if names != nil {
			t.Errorf("GetLocationNamesByIDs() names = %#v, want nil", names)
		}
	})

	t.Run("returns Mongo errors", func(t *testing.T) {
		mt := useLocationsMockMongo(t)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{
			Code:    2,
			Message: "bad value",
		}))

		names, err := GetLocationNamesByIDs(context.Background(), []string{"location-1"})
		if err == nil || !strings.Contains(err.Error(), "bad value") {
			t.Fatalf("GetLocationNamesByIDs() error = %v, want Mongo error", err)
		}
		if names != nil {
			t.Errorf("GetLocationNamesByIDs() names = %#v, want nil", names)
		}
	})
}

func useLocationsMockMongo(t *testing.T) *mtest.T {
	t.Helper()

	mt := mtest.New(t, mtest.NewOptions().
		ClientType(mtest.Mock).
		ShareClient(true).
		DatabaseName("test").
		CollectionName("locations"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })
	return mt
}
