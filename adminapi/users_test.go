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
	"go.mongodb.org/mongo-driver/event"
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

// Regression test: search must be regex-escaped before it reaches Mongo, or
// a caller can submit pathological regex syntax (ReDoS risk), matching the
// escaping convention already used by jobrequests/vacancy_filter.go and
// profiles/model.go.
func TestBuildUserFilterEscapesRegexMetacharactersInSearch(t *testing.T) {
	got := buildUserFilter("", "", "a.*(evil)+")
	wantRegex := `a\.\*\(evil\)\+`
	or, ok := got["$or"].([]bson.M)
	if !ok || len(or) == 0 {
		t.Fatalf("buildUserFilter() $or = %#v, want non-empty []bson.M", got["$or"])
	}
	for _, clause := range or {
		for _, v := range clause {
			field, ok := v.(bson.M)
			if !ok {
				t.Fatalf("clause value = %#v, want bson.M", v)
			}
			if field["$regex"] != wantRegex {
				t.Errorf("$regex = %q, want %q (metacharacters must be escaped)", field["$regex"], wantRegex)
			}
		}
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
	// CountDocuments issues an aggregate command internally in mongo-driver
	// v1.17, so it also needs a cursor-shaped mock response here, not a plain
	// command-ack response — using CreateSuccessResponse for this call causes
	// the handler to fail with 500 in this test.
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

func TestHandleUpdateUserTypeRejectsAdmin(t *testing.T) {
	// No mock responses queued: an invalid user_type must be rejected before
	// any DB lookup, so a real call here would panic with "no responses
	// remaining", proving the reject happens up front.
	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/user-type", `{"user_type":"admin"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserType(ctx); err != nil {
		t.Fatalf("HandleUpdateUserType() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserTypeUnknownUserReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/missing/user-type", `{"user_type":"customer"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := HandleUpdateUserType(ctx); err != nil {
		t.Fatalf("HandleUpdateUserType() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserTypeUpdatesAndReturnsUser(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"}},
	))
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "n", Value: 1},
		bson.E{Key: "nModified", Value: 1},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "customer"}},
	))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/user-type", `{"user_type":"customer"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserType(ctx); err != nil {
		t.Fatalf("HandleUpdateUserType() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleResendUserOtpRejectsAlreadyVerified(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "phone", Value: "+254700000000"}, {Key: "phone_verified", Value: true}},
	))

	ctx, rec := adminHandlerContext(http.MethodPost, "/users/user-1/resend-otp", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleResendUserOtp(ctx); err != nil {
		t.Fatalf("HandleResendUserOtp() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleResendUserOtpUnknownUserReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodPost, "/users/missing/resend-otp", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := HandleResendUserOtp(ctx); err != nil {
		t.Fatalf("HandleResendUserOtp() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleResendUserOtpSendsForUnverifiedUser(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "phone", Value: "+254700000000"}, {Key: "phone_verified", Value: false}},
	))
	// issuePhoneOTP persists the new code via UpdateUser; SMS delivery
	// failure is logged, not surfaced, so no further mock is needed for it.
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "n", Value: 1},
		bson.E{Key: "nModified", Value: 1},
	))

	ctx, rec := adminHandlerContext(http.MethodPost, "/users/user-1/resend-otp", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleResendUserOtp(ctx); err != nil {
		t.Fatalf("HandleResendUserOtp() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserContactRequiresPhoneOrEmail(t *testing.T) {
	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserContactRejectsInvalidEmail(t *testing.T) {
	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{"email":"not-an-email"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserContactUnknownUserReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/missing/contact", `{"phone":"+254711111111"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserContactNoOpWhenUnchanged(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "phone", Value: "+254700000000"}},
	))
	// No further mock responses queued: an unchanged value must not reach
	// UpdateUser, so a real call would panic with "no responses remaining".

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{"phone":"+254700000000"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateUserContactPhoneChangeResetsVerification(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "phone", Value: "+254700000000"}, {Key: "phone_verified", Value: true}},
	))
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "n", Value: 1},
		bson.E{Key: "nModified", Value: 1},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "phone", Value: "+254711111111"}, {Key: "phone_verified", Value: false}},
	))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{"phone":"+254711111111"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// GetStartedEvent() pops events FIFO (oldest-first) despite its doc
	// comment claiming "most recent" — a known mongo-driver v1.17 quirk
	// (see users/profile_status_reversion_test.go for the same workaround).
	// This handler issues find, update, find (a re-fetch after the write),
	// so the update is neither the first nor the last recorded event —
	// scan for it by command name instead of assuming a position.
	events := mt.GetAllStartedEvents()
	var updateEvent *event.CommandStartedEvent
	for _, e := range events {
		if e.CommandName == "update" {
			updateEvent = e
			break
		}
	}
	if updateEvent == nil {
		t.Fatalf("no update command found among %d recorded events", len(events))
	}
	var command struct {
		Updates []struct {
			U bson.M `bson:"u"`
		} `bson:"updates"`
	}
	if err := bson.Unmarshal(updateEvent.Command, &command); err != nil {
		t.Fatalf("decode update command: %v", err)
	}
	if len(command.Updates) != 1 {
		t.Fatalf("updates = %#v, want exactly one update", command.Updates)
	}
	set, ok := command.Updates[0].U["$set"].(bson.M)
	if !ok {
		t.Fatalf("update $set = %#v, want bson.M", command.Updates[0].U["$set"])
	}
	delete(set, "updated_at")
	want := bson.M{"phone": "+254711111111", "phone_verified": false}
	if !reflect.DeepEqual(set, want) {
		t.Errorf("update $set (excluding updated_at) = %#v, want %#v", set, want)
	}
}

func TestHandleUpdateUserContactDuplicatePhoneReturns409(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "phone", Value: "+254700000000"}},
	))
	mt.AddMockResponses(mtest.CreateWriteErrorsResponse(mtest.WriteError{
		Code:    11000,
		Message: "E11000 duplicate key error collection: test.users index: uniq_phone dup key: { phone: \"+254711111111\" }",
	}))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{"phone":"+254711111111"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "phone") {
		t.Errorf("body = %s, want a phone-specific conflict message", rec.Body.String())
	}
}

func TestHandleUpdateUserContactDuplicateEmailReturns409(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "email", Value: "old@example.com"}},
	))
	mt.AddMockResponses(mtest.CreateWriteErrorsResponse(mtest.WriteError{
		Code:    11000,
		Message: "E11000 duplicate key error collection: test.users index: uniq_email dup key: { email: \"new@example.com\" }",
	}))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{"email":"new@example.com"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "email") {
		t.Errorf("body = %s, want an email-specific conflict message", rec.Body.String())
	}
}

func TestHandleUpdateUserContactUpdatesAndReturnsUser(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "email", Value: "old@example.com"}},
	))
	mt.AddMockResponses(mtest.CreateSuccessResponse(
		bson.E{Key: "n", Value: 1},
		bson.E{Key: "nModified", Value: 1},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "email", Value: "new@example.com"}},
	))

	ctx, rec := adminHandlerContext(http.MethodPatch, "/users/user-1/contact", `{"email":"new@example.com"}`)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserContact(ctx); err != nil {
		t.Fatalf("HandleUpdateUserContact() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}
