package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// Req 4.9 / 4.10: when every matching users document is withheld from the
// Link_Candidate set, Authenticate must report why. Falling through to the
// create-a-users-document path instead would collide on the globally unique
// email index and surface as a generic conflict that tells the caller
// nothing actionable.
func TestAuthenticate_WithheldCandidatesReportWhy(t *testing.T) {
	tests := []struct {
		name                string
		userType            string
		status              string
		wantCode            string
		wantSuggestedAction string
	}{
		{
			name:                "artisan is withheld as non-employer",
			userType:            "artisan",
			status:              "active",
			wantCode:            "email_belongs_to_non_employer",
			wantSuggestedAction: "use_different_email",
		},
		{
			name:                "assessor is withheld as non-employer",
			userType:            "assessor",
			status:              "active",
			wantCode:            "email_belongs_to_non_employer",
			wantSuggestedAction: "use_different_email",
		},
		{
			name:                "admin is withheld as non-employer",
			userType:            "admin",
			status:              "active",
			wantCode:            "email_belongs_to_non_employer",
			wantSuggestedAction: "use_different_email",
		},
		{
			// Suspended takes precedence: it directs the caller to support
			// rather than to a different address.
			name:                "suspended customer is withheld as suspended",
			userType:            "customer",
			status:              "suspended",
			wantCode:            "account_suspended",
			wantSuggestedAction: "contact_support",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

			mt.Run(tt.name, func(mt *mtest.T) {
				useLinkChallengeTestMongo(mt)

				clock := &fakeClock{now: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)}
				fake := stytch.NewFake(clock)

				adapter := stytch.NewWithClient(stytch.Config{
					ProjectID:     "proj-test",
					Secret:        "secret-test",
					PublicToken:   "pub-test",
					Environment:   "test",
					WebhookSecret: "whsec_test",
					Clock:         clock,
				}, fake)
				svc := NewService(adapter, nil)

				mt.AddMockResponses(
					// The onboarding_states upsert is a FindOneAndUpdate, so
					// it needs a command reply carrying "value" — not a
					// cursor. The returned state is still at
					// awaiting_verification, so the candidate search runs.
					bson.D{
						{Key: "ok", Value: 1},
						{Key: "value", Value: bson.D{
							{Key: "_id", Value: "state-1"},
							{Key: "stage", Value: string(StageAwaitingVerification)},
							{Key: "email", Value: "user@example.com"},
							{Key: "email_verified", Value: true},
						}},
					},
					// One users document matches, and it is withheld.
					mtest.CreateCursorResponse(0, ns(mt, "users"), mtest.FirstBatch, bson.D{
						{Key: "_id", Value: "user-1"},
						{Key: "email", Value: "user@example.com"},
						{Key: "user_type", Value: tt.userType},
						{Key: "status", Value: tt.status},
					}),
				)

				_, err := svc.Authenticate(context.Background(), AuthenticateInput{
					DiscoveryToken: "tok",
					TokenType:      "magic_link",
				})
				if err == nil {
					mt.Fatal("expected an error when every candidate is withheld")
				}

				var svcErr *ServiceError
				if !errors.As(err, &svcErr) {
					mt.Fatalf("expected *ServiceError, got %T: %v", err, err)
				}
				if svcErr.Status != 409 {
					mt.Errorf("Status = %d, want 409", svcErr.Status)
				}
				if svcErr.Code != tt.wantCode {
					mt.Errorf("Code = %q, want %q", svcErr.Code, tt.wantCode)
				}
				if svcErr.SuggestedAction != tt.wantSuggestedAction {
					mt.Errorf("SuggestedAction = %q, want %q", svcErr.SuggestedAction, tt.wantSuggestedAction)
				}
			})
		})
	}
}
