package middleware

import (
	"encoding/json"
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

type roleMiddleware func(echo.HandlerFunc) echo.HandlerFunc

type roleErrorResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func TestRoleMiddlewareMissingContextUserReturnsGenericUnauthorized(t *testing.T) {
	recorder, nextCalled, err := runRoleMiddleware(IsAdmin, "")
	if err != nil {
		t.Fatalf("IsAdmin() error = %v", err)
	}
	if nextCalled {
		t.Fatal("next handler was called without a context user")
	}
	assertGenericUnauthorized(t, recorder)
}

func TestRoleMiddlewareDatabaseLookupFailureReturnsGenericUnauthorized(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("lookup failure", func(mt *mtest.T) {
		useRoleTestMongo(mt)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{
			Code:    123,
			Message: "sensitive database failure",
		}))

		recorder, nextCalled, err := runRoleMiddleware(IsAdmin, "user-1")
		if err != nil {
			mt.Fatalf("IsAdmin() error = %v", err)
		}
		if nextCalled {
			mt.Fatal("next handler was called after a user lookup failure")
		}
		assertGenericUnauthorized(mt, recorder)
		if strings.Contains(recorder.Body.String(), "sensitive database failure") {
			mt.Errorf("response leaked database error: %s", recorder.Body.String())
		}
	})
}

func TestRoleMiddlewareAllowsOnlyMatchingRole(t *testing.T) {
	tests := []struct {
		name       string
		middleware roleMiddleware
		role       string
	}{
		{name: "admin", middleware: IsAdmin, role: "admin"},
		{name: "artisan", middleware: IsArtisan, role: "artisan"},
		{name: "customer", middleware: IsCustomer, role: "customer"},
		{name: "assessor", middleware: IsAssessor, role: "assessor"},
	}

	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, test := range tests {
		test := test
		mt.Run(test.name+" accepts matching role", func(mt *mtest.T) {
			useRoleTestMongo(mt)
			mt.AddMockResponses(roleUserResponse(mt, test.role))

			recorder, nextCalled, err := runRoleMiddleware(test.middleware, "user-1")
			if err != nil {
				mt.Fatalf("middleware error = %v", err)
			}
			if !nextCalled {
				mt.Fatal("next handler was not called for matching role")
			}
			if recorder.Code != http.StatusNoContent {
				mt.Errorf("status = %d, want 204", recorder.Code)
			}
		})

		mt.Run(test.name+" rejects wrong role", func(mt *mtest.T) {
			useRoleTestMongo(mt)
			mt.AddMockResponses(roleUserResponse(mt, "wrong-role"))

			recorder, nextCalled, err := runRoleMiddleware(test.middleware, "user-1")
			if err != nil {
				mt.Fatalf("middleware error = %v", err)
			}
			if nextCalled {
				mt.Fatal("next handler was called for wrong role")
			}
			assertGenericUnauthorized(mt, recorder)
		})
	}
}

func runRoleMiddleware(middleware roleMiddleware, userID string) (*httptest.ResponseRecorder, bool, error) {
	app := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}

	nextCalled := false
	handler := middleware(func(c echo.Context) error {
		nextCalled = true
		return c.NoContent(http.StatusNoContent)
	})
	err := handler(ctx)
	return recorder, nextCalled, err
}

func assertGenericUnauthorized(t testing.TB, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", recorder.Code, recorder.Body.String())
	}
	var response roleErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Success || response.Message != "Unauthorized" {
		t.Errorf("response = %+v, want generic unauthorized response", response)
	}
}

func roleUserResponse(mt *mtest.T, role string) bson.D {
	return mtest.CreateCursorResponse(
		0,
		mt.DB.Name()+".users",
		mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: role}},
	)
}

func useRoleTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}
