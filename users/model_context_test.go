package users

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestGetUserByIDUsesCanceledCallerContext(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("canceled caller context stops lookup", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{
			Client: mt.Client,
			DB:     mt.Client.Database(mtest.TestDb),
		}
		mt.Cleanup(func() { configs.MI = previous })

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		user, err := GetUserByID(ctx, "user-1")
		if user != nil {
			mt.Errorf("GetUserByID() user = %#v, want nil", user)
		}
		if !errors.Is(err, context.Canceled) {
			mt.Fatalf("GetUserByID() error = %v, want context.Canceled", err)
		}
	})
}

func TestGetUserBySlugUsesCanceledCallerContext(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("canceled caller context stops lookup", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{
			Client: mt.Client,
			DB:     mt.Client.Database(mtest.TestDb),
		}
		mt.Cleanup(func() { configs.MI = previous })

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		user, err := GetUserBySlug(ctx, "artisan-slug")
		if user != nil {
			mt.Errorf("GetUserBySlug() user = %#v, want nil", user)
		}
		if !errors.Is(err, context.Canceled) {
			mt.Fatalf("GetUserBySlug() error = %v, want context.Canceled", err)
		}
	})
}
