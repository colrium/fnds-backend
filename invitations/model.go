package invitations

import (
	"context"
	"fmt"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Status is one of the five Invitation_Status values (Req 10).
type Status string

const (
	StatusPending  Status = "pending"
	StatusAccepted Status = "accepted"
	StatusDeclined Status = "declined"
	StatusRevoked  Status = "revoked"
	StatusExpired  Status = "expired"
)

// Invitation represents a record offering a named email address a specified
// Workspace_Role in a specified Workspace. There is no field holding the
// plaintext invitation token, on any document, at any point. TokenHash is
// json:"-", so even a handler returning the whole struct cannot leak it
// (Req 10.8, 10.17).
type Invitation struct {
	ID            string     `json:"id" bson:"_id"`
	WorkspaceID   string     `json:"workspace_id" bson:"workspace_id"`
	Email         string     `json:"email" bson:"email"`
	Role          string     `json:"role" bson:"role"`
	Status        Status     `json:"status" bson:"status"`
	TokenHash     string     `json:"-" bson:"token_hash"`
	InvitedBy     string     `json:"invited_by" bson:"invited_by"`
	IssuedAt      time.Time  `json:"issued_at" bson:"issued_at"`
	ExpiresAt     time.Time  `json:"expires_at" bson:"expires_at"`
	DeliveryState string     `json:"delivery_state" bson:"delivery_state"`
	LastSentAt    *time.Time `json:"last_sent_at,omitempty" bson:"last_sent_at,omitempty"`
	ResendCount   int        `json:"resend_count" bson:"resend_count"`
	AcceptedAt    *time.Time `json:"accepted_at,omitempty" bson:"accepted_at,omitempty"`
	AcceptedBy    string     `json:"-" bson:"accepted_user_id,omitempty"`
	DeclinedAt    *time.Time `json:"declined_at,omitempty" bson:"declined_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty" bson:"revoked_at,omitempty"`
	RevokedBy     string     `json:"-" bson:"revoked_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" bson:"updated_at"`
}

// EnsureIndexes creates the five indexes for the invitations collection.
//
//  1. uniq_invitation_token_hash — unique on token_hash with PFE excluding
//     empty/absent values so that only rows holding a real hash enforce uniqueness.
//  2. uniq_pending_invitation_workspace_email — unique on (workspace_id, email)
//     with PFE {status: "pending"}, enforcing at most one pending invitation per
//     workspace/email pair and enabling upsert-in-place (Req 10.6).
//  3. invitations_workspace_status — compound for listing by workspace and status.
//  4. invitations_pending_expiry — supports the expiry sweep job (Req 12.8).
//  5. invitations_email_status — supports checking if an email has pending invitations.
func EnsureIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("invitations")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "token_hash", Value: 1}},
			Options: options.Index().
				SetName("uniq_invitation_token_hash").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"token_hash": bson.M{"$type": "string", "$gt": ""},
				}),
		},
		{
			Keys: bson.D{
				{Key: "workspace_id", Value: 1},
				{Key: "email", Value: 1},
			},
			Options: options.Index().
				SetName("uniq_pending_invitation_workspace_email").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"status": "pending",
				}),
		},
		{
			Keys: bson.D{
				{Key: "workspace_id", Value: 1},
				{Key: "status", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("invitations_workspace_status"),
		},
		{
			Keys: bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().
				SetName("invitations_pending_expiry").
				SetPartialFilterExpression(bson.M{
					"status": "pending",
				}),
		},
		{
			Keys: bson.D{
				{Key: "email", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("invitations_email_status"),
		},
	})
	if err != nil {
		return fmt.Errorf("invitations.EnsureIndexes: %w", err)
	}
	return nil
}
