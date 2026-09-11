package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/configs"
	"backend/ratelimit"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// useLinkChallengeTestMongo swaps configs.MI to point at the mtest database
// and restores the original on cleanup, mirroring
// workspaces/post_auth_test.go's usePostAuthTestMongo.
func useLinkChallengeTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

func ns(mt *mtest.T, coll string) string {
	return mt.DB.Name() + "." + coll
}

func TestStateByID(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("found", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "state-1"},
			{Key: "stage", Value: string(StageAwaitingWorkspaceName)},
		}))

		state, err := svc.StateByID(context.Background(), "state-1")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if state.ID != "state-1" {
			mt.Errorf("ID = %q, want %q", state.ID, "state-1")
		}
		if state.Stage != StageAwaitingWorkspaceName {
			mt.Errorf("Stage = %q, want %q", state.Stage, StageAwaitingWorkspaceName)
		}
	})

	mt.Run("not found", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch))

		_, err := svc.StateByID(context.Background(), "missing")
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) {
			mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
		}
		if serviceErr.Status != 404 {
			mt.Errorf("Status = %d, want 404", serviceErr.Status)
		}
	})

	mt.Run("empty state id", func(mt *mtest.T) {
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		_, err := svc.StateByID(context.Background(), "")
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) {
			mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
		}
		if serviceErr.Status != 400 {
			mt.Errorf("Status = %d, want 400", serviceErr.Status)
		}
	})
}

func TestCompleteOnboarding(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("advances awaiting_workspace_name to complete", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns(mt, "workspaces"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "ws-1"},
				{Key: "stytch_organization_id", Value: "org-1"},
			}),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 1},
				bson.E{Key: "nModified", Value: 1},
			),
		)

		if err := svc.CompleteOnboarding(context.Background(), "state-1", "ws-1"); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
	})

	mt.Run("stage conflict when not awaiting_workspace_name", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns(mt, "workspaces"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "ws-1"},
				{Key: "stytch_organization_id", Value: "org-1"},
			}),
			mtest.CreateSuccessResponse(
				bson.E{Key: "n", Value: 0},
				bson.E{Key: "nModified", Value: 0},
			),
		)

		err := svc.CompleteOnboarding(context.Background(), "state-1", "ws-1")
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) {
			mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
		}
		if serviceErr.Status != 409 {
			mt.Errorf("Status = %d, want 409", serviceErr.Status)
		}
	})
}

func pendingPhoneOTPStateDoc() bson.D {
	return bson.D{
		{Key: "_id", Value: "state-1"},
		{Key: "stage", Value: string(StageAwaitingLinkDecision)},
		{Key: "email", Value: "employer@example.com"},
		{Key: "selected_link_candidate_id", Value: "candidate-1"},
		{Key: "challenge", Value: bson.D{
			{Key: "method", Value: string(ChallengePhoneOTP)},
			{Key: "issued_at", Value: time.Now().Add(-time.Minute)},
			{Key: "expires_at", Value: time.Now().Add(14 * time.Minute)},
			{Key: "attempts", Value: 0},
		}},
	}
}

func candidateUserDoc(otp string) bson.D {
	return bson.D{
		{Key: "_id", Value: "candidate-1"},
		{Key: "email", Value: "customer@example.com"},
		{Key: "phone", Value: "+254712345678"},
		{Key: "user_type", Value: "customer"},
		{Key: "status", Value: "active"},
		{Key: "is_active", Value: true},
		{Key: "otp", Value: otp},
	}
}

func TestIssueLinkChallenge(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("sends a fresh OTP for a pending phone_otp challenge", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch, pendingPhoneOTPStateDoc()),
			mtest.CreateCursorResponse(0, ns(mt, "users"), mtest.FirstBatch, candidateUserDoc("")),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}), // users.IssuePhoneOTP
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}), // refresh challenge
		)

		result, err := svc.IssueLinkChallenge(context.Background(), "state-1")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if result.Stage != string(StageAwaitingLinkDecision) {
			mt.Errorf("Stage = %q, want %q", result.Stage, StageAwaitingLinkDecision)
		}
		if result.Challenge != string(ChallengePhoneOTP) {
			mt.Errorf("Challenge = %q, want %q", result.Challenge, ChallengePhoneOTP)
		}
		if result.MaskedPhone == "" || result.MaskedPhone == "+254712345678" {
			mt.Errorf("MaskedPhone = %q, want a masked value", result.MaskedPhone)
		}
	})

	mt.Run("rejects when no phone_otp challenge is pending", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "state-1"},
			{Key: "stage", Value: string(StageAwaitingWorkspaceName)},
		}))

		_, err := svc.IssueLinkChallenge(context.Background(), "state-1")
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) {
			mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
		}
		if serviceErr.Status != 409 {
			mt.Errorf("Status = %d, want 409", serviceErr.Status)
		}
	})
}

func TestVerifyLinkChallenge(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("advances to awaiting_workspace_name on a correct code", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch, pendingPhoneOTPStateDoc()),
			mtest.CreateCursorResponse(0, ns(mt, "users"), mtest.FirstBatch, candidateUserDoc("123456")), // users.VerifyOTP: GetUserByID
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),  // users.VerifyOTP: UpdateUser
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),  // advance stage
		)

		result, err := svc.VerifyLinkChallenge(context.Background(), "state-1", "123456")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if result.Stage != string(StageAwaitingWorkspaceName) {
			mt.Errorf("Stage = %q, want %q", result.Stage, StageAwaitingWorkspaceName)
		}
	})

	mt.Run("increments attempts and returns 401 on an incorrect code", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch, pendingPhoneOTPStateDoc()),
			mtest.CreateCursorResponse(0, ns(mt, "users"), mtest.FirstBatch, candidateUserDoc("123456")), // users.VerifyOTP: GetUserByID
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),  // $inc attempts
		)

		_, err := svc.VerifyLinkChallenge(context.Background(), "state-1", "000000")
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) {
			mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
		}
		if serviceErr.Status != 401 {
			mt.Errorf("Status = %d, want 401", serviceErr.Status)
		}
		if serviceErr.Code != "challenge_incorrect" {
			mt.Errorf("Code = %q, want %q", serviceErr.Code, "challenge_incorrect")
		}
	})

	mt.Run("rejects an expired challenge without touching the OTP", func(mt *mtest.T) {
		useLinkChallengeTestMongo(mt)
		svc := &Service{rate: ratelimit.NewLimiter(nil, nil)}

		expired := bson.D{
			{Key: "_id", Value: "state-1"},
			{Key: "stage", Value: string(StageAwaitingLinkDecision)},
			{Key: "selected_link_candidate_id", Value: "candidate-1"},
			{Key: "challenge", Value: bson.D{
				{Key: "method", Value: string(ChallengePhoneOTP)},
				{Key: "issued_at", Value: time.Now().Add(-20 * time.Minute)},
				{Key: "expires_at", Value: time.Now().Add(-5 * time.Minute)},
				{Key: "attempts", Value: 0},
			}},
		}
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns(mt, "onboarding_states"), mtest.FirstBatch, expired))

		_, err := svc.VerifyLinkChallenge(context.Background(), "state-1", "123456")
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) {
			mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
		}
		if serviceErr.Status != 401 {
			mt.Errorf("Status = %d, want 401", serviceErr.Status)
		}
		if serviceErr.Code != "challenge_expired" {
			mt.Errorf("Code = %q, want %q", serviceErr.Code, "challenge_expired")
		}
	})
}
