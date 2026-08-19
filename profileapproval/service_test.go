package profileapproval

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useProfileApprovalMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.Cleanup(func() { configs.MI = previous })
}

// completeArtisanDocument returns a users collection document for an
// artisan whose profile is complete enough that profiles.GetProfileCompletion
// reports Complete: true, so tests can drive SubmitForReview's completeness
// gate without needing five separate empty-collection round-trips beyond
// what profiles.GetFullProfile always makes.
func userDocument(id, profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: id},
		{Key: "user_type", Value: "artisan"},
		{Key: "profile_status", Value: profileStatus},
	}
}

func completeProfileDocument(userID string) bson.D {
	return bson.D{
		{Key: "user_id", Value: userID},
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

func completeReferenceDocument(userID string) bson.D {
	return bson.D{
		{Key: "_id", Value: "ref-1"},
		{Key: "user_id", Value: userID},
		{Key: "name", Value: "Referee Name"},
		{Key: "phone_number", Value: "+254711111111"},
	}
}

func completeWorkingHourDocument(userID string) bson.D {
	return bson.D{
		{Key: "_id", Value: "wh-1"},
		{Key: "user_id", Value: userID},
		{Key: "day", Value: "monday"},
		{Key: "opening_time", Value: "08:00"},
		{Key: "closing_time", Value: "17:00"},
	}
}

// queueCompleteProfileLookups queues every Mongo round-trip
// profiles.GetFullProfile makes, in the exact order it makes them, so the
// resulting profile is Complete per profiles.GetProfileCompletion:
// users.GetUserByID, profiles.GetProfileByUserID, then Find on
// working_hours, past_employers, language_proficiencies,
// competency_references in that order.
func queueCompleteProfileLookups(mt *mtest.T, userID, profileStatus string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument(userID, profileStatus)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, completeProfileDocument(userID)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch, completeWorkingHourDocument(userID)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch, completeReferenceDocument(userID)))
}

func queueIncompleteProfileLookups(mt *mtest.T, userID, profileStatus string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument(userID, profileStatus)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch)) // no profiles row
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch))
}

func TestSubmitForReviewIncompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("incomplete", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		// SubmitForReview's own pending-check GetUserByID call, then the six
		// calls profiles.GetFullProfile makes internally (including its own
		// GetUserByID) — seven total, not six. See this task's note above
		// queueCompleteProfileLookups/queueIncompleteProfileLookups.
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "")))
		queueIncompleteProfileLookups(mt, "user-1", "")

		err := SubmitForReview(context.Background(), "user-1")
		if !errors.Is(err, ErrProfileIncomplete) {
			mt.Errorf("SubmitForReview() error = %v, want ErrProfileIncomplete", err)
		}
	})
}

func TestSubmitForReviewAlreadyPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("already pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		// Only the initial users.GetUserByID lookup is needed: the
		// already-pending check happens before GetFullProfile runs.
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "pending_review")))

		err := SubmitForReview(context.Background(), "user-1")
		if !errors.Is(err, ErrAlreadyPending) {
			mt.Errorf("SubmitForReview() error = %v, want ErrAlreadyPending", err)
		}
	})
}

func TestSubmitForReviewHappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete profile, not pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "")))
		queueCompleteProfileLookups(mt, "user-1", "")
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

		if err := SubmitForReview(context.Background(), "user-1"); err != nil {
			mt.Fatalf("SubmitForReview() error = %v", err)
		}
		// GetStartedEvent returns buffered events oldest-first (despite its doc
		// comment), and SubmitForReview issues several find commands before the
		// final update, so drain the buffer and check the last event.
		var lastEvent *event.CommandStartedEvent
		for {
			e := mt.GetStartedEvent()
			if e == nil {
				break
			}
			lastEvent = e
		}
		if lastEvent == nil || lastEvent.CommandName != "update" {
			mt.Fatalf("last started event = %#v, want an update command", lastEvent)
		}
	})
}

func TestApproveUserNotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("user not found", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

		err := Approve(context.Background(), "missing", "admin-1")
		if !errors.Is(err, ErrUserNotFound) {
			mt.Errorf("Approve() error = %v, want ErrUserNotFound", err)
		}
	})
}

func TestApproveNotPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("not pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "approved")))

		err := Approve(context.Background(), "user-1", "admin-1")
		if !errors.Is(err, ErrNotPending) {
			mt.Errorf("Approve() error = %v, want ErrNotPending", err)
		}
	})
}

func TestApproveHappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("pending to approved", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "pending_review")))
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

		if err := Approve(context.Background(), "user-1", "admin-1"); err != nil {
			mt.Fatalf("Approve() error = %v", err)
		}
	})
}

func TestRejectRequiresReason(t *testing.T) {
	if err := Reject(context.Background(), "user-1", "admin-1", "   "); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("Reject() error = %v, want ErrReasonRequired", err)
	}
}

func TestRejectUserNotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("user not found", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

		err := Reject(context.Background(), "missing", "admin-1", "missing documents")
		if !errors.Is(err, ErrUserNotFound) {
			mt.Errorf("Reject() error = %v, want ErrUserNotFound", err)
		}
	})
}

func TestRejectNotPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("not pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "rejected")))

		err := Reject(context.Background(), "user-1", "admin-1", "missing documents")
		if !errors.Is(err, ErrNotPending) {
			mt.Errorf("Reject() error = %v, want ErrNotPending", err)
		}
	})
}

func TestRejectHappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("pending to rejected", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "pending_review")))
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

		if err := Reject(context.Background(), "user-1", "admin-1", "missing documents"); err != nil {
			mt.Fatalf("Reject() error = %v", err)
		}
	})
}
