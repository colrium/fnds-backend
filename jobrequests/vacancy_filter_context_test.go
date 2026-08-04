package jobrequests

import (
	"context"
	"testing"

	"backend/configs"
	"backend/profiles"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestListAvailableJobVacanciesForWorkerProductionWrapperReturnsEmptyForMissingProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("missing profile", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
		mt.Cleanup(func() { configs.MI = previous })
		mt.AddMockResponses(mtest.CreateCursorResponse(
			0,
			mt.DB.Name()+".profiles",
			mtest.FirstBatch,
		))

		vacancies, total, err := listAvailableJobVacanciesForWorker(
			context.Background(),
			"worker-1",
			1,
			20,
		)
		if err != nil {
			mt.Fatalf("listAvailableJobVacanciesForWorker() error = %v", err)
		}
		if len(vacancies) != 0 || total != 0 {
			mt.Errorf("result = (%#v, %d), want empty vacancies and zero total", vacancies, total)
		}
	})
}

func TestListAvailableJobVacanciesForWorkerPassesContextToProfileLookup(t *testing.T) {
	type contextKey struct{}
	requestContext := context.WithValue(context.Background(), contextKey{}, "request-value")

	lookupCalled := false
	vacancies, total, err := listAvailableJobVacanciesForWorkerWithProfileLookup(
		requestContext,
		"worker-1",
		1,
		20,
		func(ctx context.Context, userID string) (*profiles.Profile, error) {
			lookupCalled = true
			if ctx != requestContext {
				t.Errorf("profile lookup context = %p, want exact request context %p", ctx, requestContext)
			}
			if got := ctx.Value(contextKey{}); got != "request-value" {
				t.Errorf("profile lookup context value = %v, want request-value", got)
			}
			if userID != "worker-1" {
				t.Errorf("profile lookup user ID = %q, want worker-1", userID)
			}
			return nil, mongo.ErrNoDocuments
		},
	)
	if err != nil {
		t.Fatalf("listAvailableJobVacanciesForWorkerWithProfileLookup() error = %v", err)
	}
	if !lookupCalled {
		t.Fatal("profile lookup was not called")
	}
	if len(vacancies) != 0 || total != 0 {
		t.Errorf("result = (%#v, %d), want empty vacancies and zero total", vacancies, total)
	}
}
