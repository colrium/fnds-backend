package onboarding

import (
	"context"
	"fmt"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Stage is one of the five closed Onboarding_State stages (Req 3.1).
//
// Transitions are NOT validated by an in-process predicate. Every transition is
// a conditional findOneAndUpdate naming the expected current stage in its
// filter (e.g. bson.M{"_id": id, "stage": StageAwaitingWorkspaceName}), so a
// request arriving in the wrong stage matches zero documents and returns 409.
// That check is atomic against concurrent requests, which a pre-flight
// in-process check would not be. Keep the filters stage-conditioned.
//
// The valid transitions are:
//   - awaiting_verification  -> awaiting_link_decision   (candidates found)
//   - awaiting_verification  -> awaiting_workspace_name  (no candidates)
//   - awaiting_link_decision -> awaiting_workspace_name  (link satisfied or create separate)
//   - awaiting_workspace_name -> provisioning            (name accepted, lease won)
//   - provisioning -> provisioning                       (retry reuses org id)
//   - provisioning -> awaiting_workspace_name            (lease expired)
//   - provisioning -> complete                           (success)
type Stage string

const (
	StageAwaitingVerification  Stage = "awaiting_verification"
	StageAwaitingLinkDecision  Stage = "awaiting_link_decision"
	StageAwaitingWorkspaceName Stage = "awaiting_workspace_name"
	StageProvisioning          Stage = "provisioning"
	StageComplete              Stage = "complete"
)

// ChallengeMethod identifies how a Link_Challenge is issued (Req 5.2, 5.3).
type ChallengeMethod string

const (
	ChallengeStytchEmailMatch ChallengeMethod = "stytch_email_match"
	ChallengePhoneOTP         ChallengeMethod = "phone_otp"
)

// Challenge holds the state of an in-flight Link_Challenge on an
// OnboardingState record.
type Challenge struct {
	Method    ChallengeMethod `bson:"method"`
	IssuedAt  time.Time       `bson:"issued_at"`
	ExpiresAt time.Time       `bson:"expires_at"` // issued_at + 15m (Req 5.4)
	Attempts  int             `bson:"attempts"`
}

// OnboardingState is the persisted progress record for one in-flight Employer
// signup, holding the values needed to resume after an interruption (Req 3.1).
//
// Every field is json:"-". The response payloads are explicit view structs, not
// this document, because it carries a live Stytch credential (the intermediate
// session token). The IntermediateSessionToken is never logged, never serialized,
// and redacted from every error.
type OnboardingState struct {
	ID                   string `json:"id" bson:"_id"`
	Stage                Stage  `json:"stage" bson:"stage"`
	Email                string `json:"-" bson:"email"`
	EmailVerified        bool   `json:"-" bson:"email_verified"`
	SignupMethod         string `json:"-" bson:"signup_method"`
	FirstName            string `json:"-" bson:"first_name,omitempty"`
	LastName             string `json:"-" bson:"last_name,omitempty"`
	UserID               string `json:"-" bson:"user_id,omitempty"`
	StytchMemberID       string `json:"-" bson:"stytch_member_id,omitempty"`
	StytchOrganizationID string `json:"-" bson:"stytch_organization_id,omitempty"`

	// Secret. Never logged, never serialized, redacted from every error. It is
	// the Stytch discovery credential that authorizes organization creation.
	IntermediateSessionToken string `json:"-" bson:"stytch_intermediate_session_token,omitempty"`

	LinkCandidateIDs       []string   `json:"-" bson:"link_candidate_ids,omitempty"`
	SelectedCandidateID    string     `json:"-" bson:"selected_link_candidate_id,omitempty"`
	Challenge              *Challenge `json:"-" bson:"challenge,omitempty"`
	WorkspaceID            string     `json:"-" bson:"workspace_id,omitempty"`
	IdempotencyKey         string     `json:"-" bson:"idempotency_key"`
	ProvisioningLeaseUntil *time.Time `json:"-" bson:"provisioning_lease_until,omitempty"`
	RescopePending         bool       `json:"-" bson:"rescope_pending,omitempty"`
	AbandonedHandled       bool       `json:"-" bson:"abandoned_handled,omitempty"`
	CreatedAt              time.Time  `json:"-" bson:"created_at"`
	UpdatedAt              time.Time  `json:"-" bson:"updated_at"`
	CompletedAt            *time.Time `json:"-" bson:"completed_at,omitempty"`
}

// EnsureIndexes creates the five indexes for the onboarding_states collection.
//
//  1. uniq_onboarding_member — unique on stytch_member_id with PFE excluding
//     empty/absent values, so re-authentication resumes rather than creating a
//     second record (Req 3.6).
//  2. uniq_onboarding_email_open — unique on email with PFE
//     {stage: "awaiting_verification"}, preventing two concurrent signups for the
//     same email from both reaching Stytch.
//  3. onboarding_stage_updated — compound for the abandoned-org reaper (Req 3.7).
//  4. onboarding_rescope_pending — supports the rescope-pending worker (Req 6.6).
//  5. onboarding_org — non-unique on stytch_organization_id for orphan detection
//     (Req 3.8, 3.9).
func EnsureIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("onboarding_states")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "stytch_member_id", Value: 1}},
			Options: options.Index().
				SetName("uniq_onboarding_member").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"stytch_member_id": bson.M{"$type": "string", "$gt": ""},
				}),
		},
		{
			Keys: bson.D{{Key: "email", Value: 1}},
			Options: options.Index().
				SetName("uniq_onboarding_email_open").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"stage": "awaiting_verification",
				}),
		},
		{
			Keys: bson.D{
				{Key: "stage", Value: 1},
				{Key: "updated_at", Value: 1},
			},
			Options: options.Index().SetName("onboarding_stage_updated"),
		},
		{
			Keys: bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().
				SetName("onboarding_rescope_pending").
				SetPartialFilterExpression(bson.M{
					"rescope_pending": true,
				}),
		},
		{
			Keys:    bson.D{{Key: "stytch_organization_id", Value: 1}},
			Options: options.Index().SetName("onboarding_org"),
		},
	})
	if err != nil {
		return fmt.Errorf("onboarding.EnsureIndexes: %w", err)
	}
	return nil
}
