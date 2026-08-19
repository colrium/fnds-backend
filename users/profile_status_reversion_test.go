package users

import (
	"context"
	"reflect"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useUsersPackageMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.Cleanup(func() { configs.MI = previous })
}

func TestRevertProfileStatusToPendingIfApprovedNoOpWhenNotApproved(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("not submitted", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		// No mock response queued: a real UpdateUser call here would panic
		// with "no responses remaining", proving this is a true no-op.
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusNotSubmitted}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
	})
	mt.Run("pending review", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusPendingReview}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
	})
	mt.Run("rejected", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusRejected}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
	})
}

func TestRevertProfileStatusToPendingIfApprovedRevertsWhenApproved(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("approved reverts to pending_review", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		mt.AddMockResponses(mtest.CreateSuccessResponse(
			bson.E{Key: "n", Value: 1},
			bson.E{Key: "nModified", Value: 1},
		))
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusApproved}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
		event := mt.GetStartedEvent()
		if event == nil || event.CommandName != "update" {
			mt.Fatalf("started event = %#v, want an update command", event)
		}

		var command struct {
			Updates []struct {
				U bson.M `bson:"u"`
			} `bson:"updates"`
		}
		if err := bson.Unmarshal(event.Command, &command); err != nil {
			mt.Fatalf("decode update command: %v", err)
		}
		if len(command.Updates) != 1 {
			mt.Fatalf("updates = %#v, want exactly one update", command.Updates)
		}
		set, ok := command.Updates[0].U["$set"].(bson.M)
		if !ok {
			mt.Fatalf("update $set = %#v, want bson.M", command.Updates[0].U["$set"])
		}
		// UpdateUser always injects updated_at itself; beyond that, the $set
		// must contain ONLY profile_status — not profile_submitted_at,
		// profile_reviewed_by, or profile_reviewed_at.
		delete(set, "updated_at")
		want := bson.M{"profile_status": ProfileStatusPendingReview}
		if !reflect.DeepEqual(set, want) {
			mt.Errorf("update $set (excluding updated_at) = %#v, want %#v (must not also set profile_submitted_at/profile_reviewed_by/profile_reviewed_at)", set, want)
		}
	})
}
