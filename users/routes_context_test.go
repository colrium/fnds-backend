package users

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/configs"
	"backend/contextutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestUserHandlersUseCanceledRequestContextForUserLookup(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		handle echo.HandlerFunc
	}{
		{name: "send OTP", method: http.MethodPost, path: "/otp/send", handle: HandleSendOtp},
		{name: "verify OTP", method: http.MethodPost, path: "/otp/verify", body: `{"otp":"123456"}`, handle: HandleVerifyOtp},
		{name: "change password", method: http.MethodPost, path: "/change-password", body: `{"current_password":"old-password","new_password":"new-password","confirm_password":"new-password"}`, handle: HandleChangePassword},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_ = useUserHandlerMockMongo(t)
			ctx, recorder := canceledUserHandlerContext(test.method, test.path, test.body)

			if err := test.handle(ctx); err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandleSetAvailabilityUsesRequestContextForPostUpdateLookup(t *testing.T) {
	mt := useUserHandlerMockMongo(t)
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "n", Value: 1},
		bson.E{Key: "nModified", Value: 1},
	))
	ctx, recorder := canceledUserHandlerContext(http.MethodPost, "/artisan/availability", `{"is_available":true}`)

	if err := HandleSetAvailability(ctx); err != nil {
		t.Fatalf("HandleSetAvailability() error = %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
}

func canceledUserHandlerContext(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(requestCtx)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	contextutil.SetUser(ctx, "user-1")
	return ctx, recorder
}

func useUserHandlerMockMongo(t *testing.T) *mtest.T {
	t.Helper()
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test").CollectionName("users"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })
	return mt
}
