package reviews

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestListReviewsContextUsesCanceledCallerContext(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("canceled context stops lookup", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
		mt.Cleanup(func() { configs.MI = previous })

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got, err := ListReviewsContext(ctx, bson.M{"artisan_id": "artisan-1"})
		if !errors.Is(err, context.Canceled) {
			mt.Fatalf("ListReviewsContext() error = %v, want context.Canceled", err)
		}
		if got != nil {
			mt.Errorf("ListReviewsContext() = %#v, want nil", got)
		}
	})
}
