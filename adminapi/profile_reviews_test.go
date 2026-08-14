package adminapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestBuildProfileReviewFilterNotSubmittedMatchesEmptyAndLiteral(t *testing.T) {
	got := buildProfileReviewFilter(users.ProfileStatusNotSubmitted)
	want := bson.M{"profile_status": bson.M{"$in": bson.A{"", users.ProfileStatusNotSubmitted}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildProfileReviewFilter(not_submitted) = %#v, want %#v", got, want)
	}
}

func TestBuildProfileReviewFilterOtherStatusesUseExactMatch(t *testing.T) {
	for _, status := range []string{
		users.ProfileStatusPendingReview,
		users.ProfileStatusApproved,
		users.ProfileStatusRejected,
	} {
		got := buildProfileReviewFilter(status)
		want := bson.M{"profile_status": status}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("buildProfileReviewFilter(%q) = %#v, want %#v", status, got, want)
		}
	}
}

func TestHandleListProfileReviewsNotSubmittedMatchesEmptyProfileStatus(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"}, {Key: "profile_status", Value: ""}},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{{Key: "n", Value: 1}}))

	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews?status=not_submitted", "")
	if err := HandleListProfileReviews(ctx); err != nil {
		t.Fatalf("HandleListProfileReviews() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Data []users.User `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Data.Data) != 1 || body.Data.Data[0].ID != "user-1" {
		t.Errorf("response data = %#v", body.Data.Data)
	}
}

func TestHandleListProfileReviewsInvalidStatusReturns400(t *testing.T) {
	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews?status=bogus", "")
	if err := HandleListProfileReviews(ctx); err != nil {
		t.Fatalf("HandleListProfileReviews() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListProfileReviewsDefaultsToPendingReview(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"}, {Key: "profile_status", Value: "pending_review"}},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{{Key: "n", Value: 1}}))

	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews", "")
	if err := HandleListProfileReviews(ctx); err != nil {
		t.Fatalf("HandleListProfileReviews() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Data []users.User `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Data.Data) != 1 || body.Data.Data[0].ProfileStatus != "pending_review" {
		t.Errorf("response data = %#v", body.Data.Data)
	}
}

func TestHandleGetProfileReviewNotFound(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews/missing", "")
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("missing")

	if err := HandleGetProfileReview(ctx); err != nil {
		t.Fatalf("HandleGetProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleApproveProfileReviewNotPending(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "profile_status", Value: "approved"}},
	))

	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/user-1/approve", "")
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("user-1")

	if err := HandleApproveProfileReview(ctx); err != nil {
		t.Fatalf("HandleApproveProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleApproveProfileReviewMissingUserReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/missing/approve", "")
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("missing")

	if err := HandleApproveProfileReview(ctx); err != nil {
		t.Fatalf("HandleApproveProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRejectProfileReviewMissingUserReturns404(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/missing/reject", `{"reason":"missing documents"}`)
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("missing")

	if err := HandleRejectProfileReview(ctx); err != nil {
		t.Fatalf("HandleRejectProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRejectProfileReviewRequiresReason(t *testing.T) {
	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/user-1/reject", `{"reason":""}`)
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("user-1")

	if err := HandleRejectProfileReview(ctx); err != nil {
		t.Fatalf("HandleRejectProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRejectProfileReviewHappyPathDoesNotFailOnNotificationError(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	// profileapproval.Reject's own GetUserByID + UpdateUser:
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "profile_status", Value: "pending_review"}},
	))
	mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/user-1/reject", `{"reason":"missing documents"}`)
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("user-1")

	if err := HandleRejectProfileReview(ctx); err != nil {
		t.Fatalf("HandleRejectProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Profile rejected") {
		t.Errorf("body = %s, want a success message", rec.Body.String())
	}
}
