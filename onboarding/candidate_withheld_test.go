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

// Req 5.12: an existing Account_Link means this person already onboarded, so
// Authenticate must sign them in and create zero additional users documents.
// Before this, an already-linked user was dropped from the candidate set with
// no record of why, fell through to the create path, and hit a raw duplicate
// key on the unique email index — signing in looked like a failed attempt to
// register an account that already existed.
func TestAuthenticate_ExistingAccountLinkSignsIn(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("returning employer is signed in, not re-registered", func(mt *mtest.T) {
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
			// State exists but is NOT at complete, so the resume shortcut does
			// not fire and the candidate search runs — the exact situation that
			// used to end in a duplicate-key error.
			bson.D{
				{Key: "ok", Value: 1},
				{Key: "value", Value: bson.D{
					{Key: "_id", Value: "state-1"},
					{Key: "stage", Value: string(StageAwaitingVerification)},
					{Key: "email", Value: "user@example.com"},
					{Key: "email_verified", Value: true},
				}},
			},
			// The users document for this email, an ordinary customer.
			mtest.CreateCursorResponse(0, ns(mt, "users"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "user-1"},
				{Key: "email", Value: "user@example.com"},
				{Key: "user_type", Value: "customer"},
				{Key: "status", Value: "active"},
			}),
			// ...which already holds an Account_Link.
			mtest.CreateCursorResponse(0, ns(mt, "account_links"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "link-1"},
				{Key: "user_id", Value: "user-1"},
				{Key: "stytch_member_id", Value: "member-1"},
				{Key: "stytch_organization_id", Value: "org-1"},
				{Key: "linked_email", Value: "user@example.com"},
			}),
		)

		result, err := svc.Authenticate(context.Background(), AuthenticateInput{
			DiscoveryToken: "tok",
			TokenType:      "magic_link",
		})
		if err != nil {
			mt.Fatalf("a linked user should sign in, got error: %v", err)
		}

		if result.Stage != string(StageComplete) {
			mt.Errorf("Stage = %q, want %q", result.Stage, StageComplete)
		}
		if result.UserID != "user-1" {
			mt.Errorf("UserID = %q, want %q", result.UserID, "user-1")
		}
		if result.StytchOrganizationID != "org-1" {
			mt.Errorf("StytchOrganizationID = %q, want %q", result.StytchOrganizationID, "org-1")
		}
		// Email must be carried: ResolvePostAuth needs it to find pending
		// invitations addressed to this person.
		if result.Email != "user@example.com" {
			mt.Errorf("Email = %q, want %q", result.Email, "user@example.com")
		}
		// The HTTP layer exchanges this for a real workspace session.
		if result.IntermediateSessionToken == "" {
			mt.Error("IntermediateSessionToken must be carried so a session can be exchanged")
		}
		// No candidates offered: this person is signing in, not linking.
		if len(result.LinkCandidates) != 0 {
			mt.Errorf("LinkCandidates = %d, want 0", len(result.LinkCandidates))
		}
	})
}

// A linked account must win over a withheld one: with legacy case-variant
// duplicates, an artisan record sharing the address must not block the
// returning Employer from signing in.
func TestAuthenticate_LinkedUserWinsOverWithheld(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("linked customer signs in despite an artisan sharing the address", func(mt *mtest.T) {
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
			bson.D{
				{Key: "ok", Value: 1},
				{Key: "value", Value: bson.D{
					{Key: "_id", Value: "state-1"},
					{Key: "stage", Value: string(StageAwaitingVerification)},
					{Key: "email", Value: "user@example.com"},
					{Key: "email_verified", Value: true},
				}},
			},
			// Two documents share the address: an artisan (withheld) and a
			// linked customer (the returning Employer).
			mtest.CreateCursorResponse(0, ns(mt, "users"), mtest.FirstBatch,
				bson.D{
					{Key: "_id", Value: "artisan-1"},
					{Key: "email", Value: "User@example.com"},
					{Key: "user_type", Value: "artisan"},
					{Key: "status", Value: "active"},
				},
				bson.D{
					{Key: "_id", Value: "user-1"},
					{Key: "email", Value: "user@example.com"},
					{Key: "user_type", Value: "customer"},
					{Key: "status", Value: "active"},
				},
			),
			// Only the customer is checked for a link (the artisan short-circuits).
			mtest.CreateCursorResponse(0, ns(mt, "account_links"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "link-1"},
				{Key: "user_id", Value: "user-1"},
				{Key: "stytch_organization_id", Value: "org-1"},
			}),
		)

		result, err := svc.Authenticate(context.Background(), AuthenticateInput{
			DiscoveryToken: "tok",
			TokenType:      "magic_link",
		})
		if err != nil {
			mt.Fatalf("linked user should sign in even alongside a withheld record, got: %v", err)
		}
		if result.Stage != string(StageComplete) {
			mt.Errorf("Stage = %q, want %q", result.Stage, StageComplete)
		}
		if result.UserID != "user-1" {
			mt.Errorf("UserID = %q, want the linked customer %q", result.UserID, "user-1")
		}
	})
}
