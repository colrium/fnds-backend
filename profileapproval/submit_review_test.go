package profileapproval

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func runSubmitReviewRequest(userID string) (*httptest.ResponseRecorder, error) {
	req := httptest.NewRequest(http.MethodPost, "/profile/submit-review", nil)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}
	return rec, HandleSubmitProfileForReview(ctx)
}

func TestHandleSubmitProfileForReviewRequiresAuth(t *testing.T) {
	recorder, err := runSubmitReviewRequest("")
	if err != nil {
		t.Fatalf("HandleSubmitProfileForReview() error = %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}

func TestHandleSubmitProfileForReviewIncompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("incomplete", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		// SubmitForReview's own pending-check GetUserByID call, then the six
		// calls profiles.GetFullProfile makes internally (including its own
		// GetUserByID) — seven total, not six. See Task 4's identical note.
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"},
		}))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"},
		}))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch)) // no profile row
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch))

		recorder, err := runSubmitReviewRequest("user-1")
		if err != nil {
			mt.Fatalf("HandleSubmitProfileForReview() error = %v", err)
		}
		if recorder.Code != http.StatusBadRequest {
			mt.Errorf("status = %d, want 400; body: %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestHandleSubmitProfileForReviewAlreadyPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("already pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"},
			{Key: "profile_status", Value: "pending_review"},
		}))

		recorder, err := runSubmitReviewRequest("user-1")
		if err != nil {
			mt.Fatalf("HandleSubmitProfileForReview() error = %v", err)
		}
		if recorder.Code != http.StatusConflict {
			mt.Errorf("status = %d, want 409; body: %s", recorder.Code, recorder.Body.String())
		}
	})
}
