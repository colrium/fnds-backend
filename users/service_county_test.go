package users

import (
	"context"
	"errors"
	"testing"

	"backend/configs"
	"backend/counties"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestUpdateProfileCountyValidation(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("rejects invalid county before database lookup", func(mt *mtest.T) {
		useUsersServiceMockMongo(mt)

		err := UpdateProfile(context.Background(), "user-1", ProfileUpdateRequest{County: "California"})
		if !errors.Is(err, counties.ErrInvalidCounty) {
			mt.Fatalf("UpdateProfile() error = %v, want counties.ErrInvalidCounty", err)
		}
		if events := mt.GetAllStartedEvents(); len(events) != 0 {
			mt.Fatalf("invalid county triggered %d MongoDB command(s), want 0", len(events))
		}
	})

	mt.Run("persists canonical county", func(mt *mtest.T) {
		useUsersServiceMockMongo(mt)
		mt.AddMockResponses(
			mtest.CreateCursorResponse(
				0,
				mt.DB.Name()+".users",
				mtest.FirstBatch,
				bson.D{{Key: "_id", Value: "user-1"}},
			),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		err := UpdateProfile(context.Background(), "user-1", ProfileUpdateRequest{County: "Uasin\tGishu County"})
		if err != nil {
			mt.Fatalf("UpdateProfile() error = %v", err)
		}

		for _, event := range mt.GetAllStartedEvents() {
			if event.CommandName != "update" || event.Command.Lookup("update").StringValue() != "profiles" {
				continue
			}
			updates, err := event.Command.Lookup("updates").Array().Values()
			if err != nil || len(updates) != 1 {
				mt.Fatalf("decode profile updates: values=%v error=%v", updates, err)
			}
			got := updates[0].Document().Lookup("u").Document().Lookup("$set").Document().Lookup("county").StringValue()
			if got != "Uasin Gishu" {
				mt.Fatalf("persisted county = %q, want Uasin Gishu", got)
			}
			return
		}
		mt.Fatal("profile update command was not recorded")
	})
}

func useUsersServiceMockMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}
