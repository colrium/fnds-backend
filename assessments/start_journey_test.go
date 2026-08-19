package assessments

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useStartJourneyMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.Cleanup(func() { configs.MI = previous })
}

func startJourneyUserDocument(profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "user_type", Value: "artisan"},
		{Key: "profile_status", Value: profileStatus},
	}
}

func startJourneyCompleteProfileDocument() bson.D {
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

func queueStartJourneyCompleteProfile(mt *mtest.T, profileStatus string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, startJourneyUserDocument(profileStatus)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, startJourneyCompleteProfileDocument()))
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
}

func TestStartJourneyRejectsUnapprovedCompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete but not approved", func(mt *mtest.T) {
		useStartJourneyMockMongo(mt)
		queueStartJourneyCompleteProfile(mt, "pending_review")

		_, err := StartJourney(context.Background(), "user-1")
		if !errors.Is(err, ErrProfileNotApproved) {
			mt.Errorf("StartJourney() error = %v, want ErrProfileNotApproved", err)
		}
	})
}

func TestStartJourneyAllowsApprovedCompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete and approved", func(mt *mtest.T) {
		useStartJourneyMockMongo(mt)
		queueStartJourneyCompleteProfile(mt, "approved")
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		journey, err := StartJourney(context.Background(), "user-1")
		if err != nil {
			mt.Fatalf("StartJourney() error = %v", err)
		}
		if journey.CurrentStage != StageProfileReview {
			mt.Errorf("CurrentStage = %q, want %q", journey.CurrentStage, StageProfileReview)
		}
	})
}
