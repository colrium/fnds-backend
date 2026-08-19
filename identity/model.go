package identity

import (
	"context"
	"fmt"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AccountLink is the persisted primary association between one Fundis users
// document and one Stytch_Member. The Stytch_Member referenced here is the one
// in the Workspace where the link was established (Req 5.8). Per-workspace
// Member identifiers live on the Membership document instead.
//
// Two unique indexes enforce that one users document holds at most one
// AccountLink and one Stytch_Member is referenced by at most one AccountLink
// (both directions of Req 5.8).
type AccountLink struct {
	ID                   string    `json:"id" bson:"_id"`
	UserID               string    `json:"user_id" bson:"user_id"`
	StytchMemberID       string    `json:"-" bson:"stytch_member_id"`
	StytchOrganizationID string    `json:"-" bson:"stytch_organization_id"`
	LinkedEmail          string    `json:"linked_email" bson:"linked_email"`
	LinkMethod           string    `json:"link_method" bson:"link_method"`
	ReviewRequired       bool      `json:"-" bson:"review_required,omitempty"`
	CreatedAt            time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt            time.Time `json:"updated_at" bson:"updated_at"`
}

// EnsureIndexes creates the four indexes for the account_links collection.
//
//  1. uniq_account_link_user — unique on user_id, enforcing one link per user.
//  2. uniq_account_link_member — unique on stytch_member_id, enforcing one link
//     per Stytch Member (both uniqueness directions of Req 5.8).
//  3. account_links_email — non-unique on linked_email for email-change
//     reconciliation lookups (Req 5.13, 21.11).
//  4. account_links_review — on updated_at descending with PFE
//     {review_required: true} for the manual review queue (Req 5.14).
func EnsureIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("account_links")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().
				SetName("uniq_account_link_user").
				SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "stytch_member_id", Value: 1}},
			Options: options.Index().
				SetName("uniq_account_link_member").
				SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "linked_email", Value: 1}},
			Options: options.Index().SetName("account_links_email"),
		},
		{
			Keys: bson.D{{Key: "updated_at", Value: -1}},
			Options: options.Index().
				SetName("account_links_review").
				SetPartialFilterExpression(bson.M{
					"review_required": true,
				}),
		},
	})
	if err != nil {
		return fmt.Errorf("identity.EnsureIndexes: %w", err)
	}
	return nil
}
