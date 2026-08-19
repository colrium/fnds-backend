package jobrequests

import (
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

func useJobApplicationMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.Cleanup(func() { configs.MI = previous })
}

func jobApplicationUserDocument(profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "user_type", Value: "artisan"},
		{Key: "profile_status", Value: profileStatus},
	}
}

func jobApplicationCompleteProfileDocument() bson.D {
	return bson.D{
		{Key: "user_id", Value: "user-1"},
		{Key: "id_number", Value: "12345678"},
		{Key: "date_of_birth", Value: "1990-01-01"},
		{Key: "gender", Value: "female"},
		{Key: "address", Value: "123 Main St"},
		{Key: "skills", Value: bson.A{"plumbing"}},
		{Key: "skill_acquisition_mode", Value: "apprenticeship"},
		{Key: "next_of_kin_name", Value: "Jane Doe"},
		{Key: "next_of_kin_phone_number", Value: "+254700000000"},
		{Key: "next_of_kin_relationship", Value: "sister"},
		{Key: "locations", Value: bson.A{"Nairobi"}},
	}
}

func TestHandleCreateJobApplicationRejectsUnapprovedCompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete but not approved", func(mt *mtest.T) {
		useJobApplicationMockMongo(mt)
		// The handler's own artisan-type-check GetUserByID call, then the
		// six calls profiles.GetFullProfile makes internally (including its
		// own GetUserByID) — seven total, not six.
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, jobApplicationUserDocument("pending_review")))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, jobApplicationUserDocument("pending_review")))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, jobApplicationCompleteProfileDocument()))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "wh-1"}, {Key: "user_id", Value: "user-1"},
			{Key: "day", Value: "monday"}, {Key: "opening_time", Value: "08:00"}, {Key: "closing_time", Value: "17:00"},
		}))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "ref-1"}, {Key: "user_id", Value: "user-1"},
			{Key: "name", Value: "Referee Name"}, {Key: "phone_number", Value: "+254711111111"},
		}))

		req := httptest.NewRequest(http.MethodPost, "/job-applications", strings.NewReader(`{"vacancy_id":"vac-1","cover_letter":"hello"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		ctx := echo.New().NewContext(req, rec)
		contextutil.SetUser(ctx, "user-1")

		if err := HandleCreateJobApplication(ctx); err != nil {
			mt.Fatalf("HandleCreateJobApplication() error = %v", err)
		}
		if rec.Code != http.StatusForbidden {
			mt.Errorf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
		}
	})
}
