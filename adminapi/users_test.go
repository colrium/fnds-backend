package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"backend/configs"
	"backend/contextutil"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useAdminMockMongo(t *testing.T, collection string) *mtest.T {
	t.Helper()
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test").CollectionName(collection))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })
	return mt
}

func adminHandlerContext(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	contextutil.SetUser(ctx, "admin-1")
	return ctx, rec
}

func TestBuildUserFilterAppliesUserTypeStatusAndSearch(t *testing.T) {
	got := buildUserFilter("artisan", "active", "jane")
	want := bson.M{
		"user_type": "artisan",
		"status":    "active",
		"$or": []bson.M{
			{"first_name": bson.M{"$regex": "jane", "$options": "i"}},
			{"last_name": bson.M{"$regex": "jane", "$options": "i"}},
			{"email": bson.M{"$regex": "jane", "$options": "i"}},
			{"phone": bson.M{"$regex": "jane", "$options": "i"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildUserFilter() = %#v, want %#v", got, want)
	}
}

func TestBuildUserFilterEmptyArgsReturnsEmptyFilter(t *testing.T) {
	got := buildUserFilter("", "", "")
	if len(got) != 0 {
		t.Errorf("buildUserFilter() = %#v, want empty", got)
	}
}

func TestHandleListUsersReturnsPagedEnvelopeWithoutPassword(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{
			{Key: "_id", Value: "user-1"},
			{Key: "first_name", Value: "Jane"},
			{Key: "last_name", Value: "Doe"},
			{Key: "user_type", Value: "artisan"},
			{Key: "status", Value: "active"},
			{Key: "password", Value: []byte("super-secret-hash")},
		},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{{Key: "n", Value: 1}},
	))

	ctx, rec := adminHandlerContext(http.MethodGet, "/users", "")
	if err := HandleListUsers(ctx); err != nil {
		t.Fatalf("HandleListUsers() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret-hash") {
		t.Fatalf("response leaked password hash: %s", rec.Body.String())
	}

	var body struct {
		Data struct {
			Data  []users.User `json:"data"`
			Total int64        `json:"total"`
			Page  int64        `json:"page"`
			Limit int64        `json:"limit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Data.Data) != 1 || body.Data.Data[0].ID != "user-1" {
		t.Errorf("response data = %#v", body.Data.Data)
	}
	if body.Data.Total != 1 {
		t.Errorf("total = %d, want 1", body.Data.Total)
	}
}

func TestHandleGetUserNotFoundReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodGet, "/users/missing", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := HandleGetUser(ctx); err != nil {
		t.Fatalf("HandleGetUser() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserStatusRejectsInvalidStatus(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "status", Value: "active"}},
	))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/status", `{"status":"deleted"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserStatus(ctx); err != nil {
		t.Fatalf("HandleUpdateUserStatus() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserStatusUnknownUserReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/missing/status", `{"status":"suspended"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := HandleUpdateUserStatus(ctx); err != nil {
		t.Fatalf("HandleUpdateUserStatus() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserStatusUpdatesAndReturnsUser(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "status", Value: "active"}},
	))
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "n", Value: 1},
		bson.E{Key: "nModified", Value: 1},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0,
		"test.users",
		mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "status", Value: "suspended"}},
	))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/status", `{"status":"suspended"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserStatus(ctx); err != nil {
		t.Fatalf("HandleUpdateUserStatus() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}
