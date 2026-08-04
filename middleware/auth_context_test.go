package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/configs"
	"backend/jwtutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestJWTAuthUsesCanceledRequestContextForUserLookup(t *testing.T) {
	t.Setenv("SESSION_KEY", "jwt-auth-context-test-secret")
	token, _, err := jwtutil.GenerateToken("user-1")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("canceled request", func(mt *mtest.T) {
		previous := configs.MI
		configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
		mt.Cleanup(func() { configs.MI = previous })

		requestCtx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequest(http.MethodGet, "/protected", nil).WithContext(requestCtx)
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		nextCalled := false

		err := JWTAuth(func(echo.Context) error {
			nextCalled = true
			return nil
		})(ctx)
		if err != nil {
			mt.Fatalf("JWTAuth() error = %v", err)
		}
		if recorder.Code != http.StatusUnauthorized || nextCalled {
			mt.Errorf("result = (status=%d, nextCalled=%v), want (401, false)", recorder.Code, nextCalled)
		}
	})
}
