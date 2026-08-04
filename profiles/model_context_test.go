package profiles

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestGetProfileByUserIDUsesCanceledCallerContext(t *testing.T) {
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

		profile, err := GetProfileByUserID(ctx, "user-1")
		if profile != nil {
			mt.Errorf("GetProfileByUserID() profile = %#v, want nil", profile)
		}
		if !errors.Is(err, context.Canceled) {
			mt.Fatalf("GetProfileByUserID() error = %v, want context.Canceled", err)
		}
	})
}
