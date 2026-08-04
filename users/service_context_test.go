package users

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestUserServicesUseCanceledCallerContext(t *testing.T) {
	tests := []struct {
		name      string
		invoke    func(context.Context) error
		wantCause bool
	}{
		{name: "SendOTP", invoke: func(ctx context.Context) error { return SendOTP(ctx, "user-1") }, wantCause: true},
		{name: "ResendOTP", invoke: func(ctx context.Context) error { return ResendOTP(ctx, "user-1") }, wantCause: true},
		{name: "VerifyOTP", invoke: func(ctx context.Context) error { return VerifyOTP(ctx, "user-1", "123456") }, wantCause: true},
		{name: "GetProfile", invoke: func(ctx context.Context) error { _, err := GetProfile(ctx, "user-1"); return err }},
		{name: "UpdateProfile", invoke: func(ctx context.Context) error { return UpdateProfile(ctx, "user-1", ProfileUpdateRequest{}) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test").CollectionName("users"))
			previous := configs.MI
			configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
			t.Cleanup(func() { configs.MI = previous })

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := test.invoke(ctx)
			if err == nil {
				t.Fatal("service error = nil, want canceled lookup failure")
			}
			if test.wantCause && !errors.Is(err, context.Canceled) {
				t.Errorf("service error = %v, want context.Canceled", err)
			}
		})
	}
}
