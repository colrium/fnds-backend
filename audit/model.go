package audit

import (
	"context"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Actions is the closed set of auditable actions (Req 23.1).
const (
	ActionWorkspaceCreated        = "workspace.created"
	ActionWorkspaceRenamed        = "workspace.renamed"
	ActionWorkspaceDeletionState  = "workspace.deletion_state_changed"
	ActionInvitationCreated       = "invitation.created"
	ActionInvitationRevoked       = "invitation.revoked"
	ActionInvitationAccepted      = "invitation.accepted"
	ActionMembershipCreated       = "membership.created"
	ActionRoleChanged             = "membership.role_changed"
	ActionStatusChanged           = "membership.status_changed"
	ActionOwnershipTransferred    = "ownership.transferred"
	ActionAccountLinkCreated      = "account_link.created"
	ActionAccountLinkRemoved      = "account_link.removed"
	ActionAccountLinkEmailChanged = "account_link.email_changed"
	ActionPaymentMethodChanged    = "billing.payment_method_changed"
)

// Entry is an append-only audit log record naming the actor, workspace,
// action, target, and time. Requirements: 23.2, 23.4.
type Entry struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspace_id" json:"workspace_id"`
	ActorUserID string    `bson:"actor_user_id" json:"actor_user_id"`
	ActorKind   string    `bson:"actor_kind" json:"actor_kind"` // member | platform_admin | system
	Action      string    `bson:"action" json:"action"`
	TargetKind  string    `bson:"target_kind" json:"target_kind"`
	TargetID    string    `bson:"target_id" json:"target_id"`
	IP          string    `bson:"ip" json:"ip"`
	Previous    any       `bson:"previous,omitempty" json:"previous,omitempty"`
	New         any       `bson:"new,omitempty" json:"new,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
}

// EnsureIndexes creates the indexes for the audit_log_entries collection.
// Called once at startup from main.go. Idempotent.
// Requirements: 23.2, 23.4.
func EnsureIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("audit_log_entries")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// audit_workspace_time — workspace + time descending for paginated reads
			Keys:    bson.D{{Key: "workspace_id", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("audit_workspace_time"),
		},
		{
			// audit_target — target kind + id + time descending for target history
			Keys:    bson.D{{Key: "target_kind", Value: 1}, {Key: "target_id", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("audit_target"),
		},
	})
	return err
}
