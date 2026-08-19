package workspaces

import (
	"context"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DeletionState represents the lifecycle state of workspace deletion.
type DeletionState string

const (
	DeletionNone      DeletionState = "" // stored as an absent field
	DeletionScheduled DeletionState = "scheduled"
	DeletionDeleted   DeletionState = "deleted"
)

// MembershipStatus represents the status of a workspace membership.
type MembershipStatus string

const (
	MembershipActive    MembershipStatus = "active"
	MembershipSuspended MembershipStatus = "suspended"
	MembershipRemoved   MembershipStatus = "removed"
)

// Workspace is the first-class multi-user tenant (Req 2.10, 2.14).
type Workspace struct {
	ID                   string        `json:"id" bson:"_id"`
	Name                 string        `json:"name" bson:"name"`
	NameNormalized       string        `json:"-" bson:"name_normalized"`
	Slug                 string        `json:"slug" bson:"slug"`
	StytchOrganizationID string        `json:"-" bson:"stytch_organization_id"`
	CreatedBy            string        `json:"created_by" bson:"created_by"`
	DeletionState        DeletionState `json:"deletion_state,omitempty" bson:"deletion_state,omitempty"`
	DeletionRequestedBy  string        `json:"-" bson:"deletion_requested_by,omitempty"`
	DeletionEffectiveAt  *time.Time    `json:"deletion_effective_at,omitempty" bson:"deletion_effective_at,omitempty"`
	LastWebhookEventAt   *time.Time    `json:"-" bson:"last_webhook_event_at,omitempty"`
	RescopePending       bool          `json:"rescope_pending,omitempty" bson:"rescope_pending,omitempty"`
	CreatedAt            time.Time     `json:"created_at" bson:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at" bson:"updated_at"`
}

// Membership records a user's role in a workspace (Req 14.1, 14.2, 15.1, 15.2).
type Membership struct {
	ID                        string           `json:"id" bson:"_id"`
	WorkspaceID               string           `json:"workspace_id" bson:"workspace_id"`
	UserID                    string           `json:"user_id" bson:"user_id"`
	Role                      Role             `json:"role" bson:"role"`
	Status                    MembershipStatus `json:"status" bson:"status"`
	AuthorityVersion          int64            `json:"authority_version" bson:"authority_version"`
	StytchMemberID            string           `json:"-" bson:"stytch_member_id,omitempty"`
	InvitedBy                 string           `json:"invited_by,omitempty" bson:"invited_by,omitempty"`
	InvitationID              string           `json:"-" bson:"invitation_id,omitempty"`
	JoinedAt                  time.Time        `json:"joined_at" bson:"joined_at"`
	LastActiveAt              time.Time        `json:"-" bson:"last_active_at"`
	StytchDeletePending       bool             `json:"-" bson:"stytch_member_delete_pending,omitempty"`
	StytchDeleteAttempts      int              `json:"-" bson:"stytch_member_delete_attempts,omitempty"`
	StytchDeleteFirstFailedAt *time.Time       `json:"-" bson:"stytch_member_delete_first_failed_at,omitempty"`
	LastWebhookEventAt        *time.Time       `json:"-" bson:"last_webhook_event_at,omitempty"`
	CreatedAt                 time.Time        `json:"created_at" bson:"created_at"`
	UpdatedAt                 time.Time        `json:"updated_at" bson:"updated_at"`
}

// EnsureIndexes creates the workspace and membership indexes from the design
// catalogue. Called once at startup from main.go. Idempotent.
// Requirements: 2.10, 2.14, 14.1, 14.2, 15.1, 15.2, 16.3, 19.7
func EnsureIndexes(ctx context.Context) error {
	// ─── workspaces collection ──────────────────────────────────────────
	workspacesColl := configs.MI.DB.Collection("workspaces")

	_, err := workspacesColl.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// 1. uniq_workspace_slug — {slug: 1} unique
			Keys:    bson.D{{Key: "slug", Value: 1}},
			Options: options.Index().SetName("uniq_workspace_slug").SetUnique(true),
		},
		{
			// 2. uniq_workspace_stytch_org — {stytch_organization_id: 1} unique
			Keys:    bson.D{{Key: "stytch_organization_id", Value: 1}},
			Options: options.Index().SetName("uniq_workspace_stytch_org").SetUnique(true),
		},
		{
			// 3. workspaces_name_normalized — {name_normalized: 1} non-unique
			Keys:    bson.D{{Key: "name_normalized", Value: 1}},
			Options: options.Index().SetName("workspaces_name_normalized"),
		},
		{
			// 4. workspaces_created_by — {created_by: 1, created_at: -1} non-unique
			Keys:    bson.D{{Key: "created_by", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("workspaces_created_by"),
		},
		{
			// 5. workspaces_deletion_due — {deletion_state: 1, deletion_effective_at: 1}
			//    with PFE {deletion_state: {$exists: true}}
			Keys: bson.D{{Key: "deletion_state", Value: 1}, {Key: "deletion_effective_at", Value: 1}},
			Options: options.Index().SetName("workspaces_deletion_due").
				SetPartialFilterExpression(bson.M{"deletion_state": bson.M{"$exists": true}}),
		},
	})
	if err != nil {
		return err
	}

	// ─── memberships collection ─────────────────────────────────────────
	membershipsColl := configs.MI.DB.Collection("memberships")

	_, err = membershipsColl.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// 1. uniq_membership_user_workspace — {user_id: 1, workspace_id: 1} unique
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "workspace_id", Value: 1}},
			Options: options.Index().SetName("uniq_membership_user_workspace").SetUnique(true),
		},
		{
			// 2. uniq_active_owner_per_workspace — {workspace_id: 1} unique
			//    with PFE {role: "owner", status: "active"}
			//    Enforces exactly one active Owner per workspace (Req 14.1, 14.2).
			Keys: bson.D{{Key: "workspace_id", Value: 1}},
			Options: options.Index().SetName("uniq_active_owner_per_workspace").SetUnique(true).
				SetPartialFilterExpression(bson.M{"role": "owner", "status": "active"}),
		},
		{
			// 3. uniq_membership_stytch_member — {stytch_member_id: 1} unique
			//    with PFE {stytch_member_id: {$type: "string", $gt: ""}}
			Keys: bson.D{{Key: "stytch_member_id", Value: 1}},
			Options: options.Index().SetName("uniq_membership_stytch_member").SetUnique(true).
				SetPartialFilterExpression(bson.M{"stytch_member_id": bson.M{"$type": "string", "$gt": ""}}),
		},
		{
			// 4. memberships_workspace_status_role — {workspace_id: 1, status: 1, role: 1} non-unique
			Keys:    bson.D{{Key: "workspace_id", Value: 1}, {Key: "status", Value: 1}, {Key: "role", Value: 1}},
			Options: options.Index().SetName("memberships_workspace_status_role"),
		},
		{
			// 5. memberships_user_status_active — {user_id: 1, status: 1, last_active_at: -1} non-unique
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "status", Value: 1}, {Key: "last_active_at", Value: -1}},
			Options: options.Index().SetName("memberships_user_status_active"),
		},
		{
			// 6. memberships_stytch_delete_pending — {stytch_member_delete_first_failed_at: 1}
			//    with PFE {stytch_member_delete_pending: true}
			Keys: bson.D{{Key: "stytch_member_delete_first_failed_at", Value: 1}},
			Options: options.Index().SetName("memberships_stytch_delete_pending").
				SetPartialFilterExpression(bson.M{"stytch_member_delete_pending": true}),
		},
	})
	return err
}
