package workspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/stytch"
	"backend/users"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MembershipError is a structured error returned by MembershipService operations
// when a precondition or validation check fails.
type MembershipError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *MembershipError) Error() string { return e.Message }

// MemberView is the public representation of a membership for list endpoints (Req 13.15).
type MemberView struct {
	ID        string           `json:"id"`
	UserID    string           `json:"user_id"`
	FirstName string           `json:"first_name"`
	LastName  string           `json:"last_name"`
	Email     string           `json:"email"`
	Role      Role             `json:"role"`
	Status    MembershipStatus `json:"status"`
	JoinedAt  time.Time        `json:"joined_at"`
}

// CreateMembershipInput carries the parameters for creating a membership.
type CreateMembershipInput struct {
	WorkspaceID    string
	UserID         string
	Role           Role
	StytchMemberID string
	InvitedBy      string
	InvitationID   string
}

// MembershipService manages membership lifecycle.
type MembershipService struct {
	adapter *stytch.Adapter
	audit   *audit.Logger
	clock   stytch.Clock
	// notifier delivers the membership emails of Req 13.6, 13.9, and 14.9. It
	// defaults to the production email notifier; tests substitute a spy.
	notifier  Notifier
	txCapable bool
}

// NewMembershipService constructs a MembershipService.
// txCapable indicates whether the connected MongoDB deployment supports multi-document
// transactions (determined at startup by configs.TransactionsSupported).
func NewMembershipService(adapter *stytch.Adapter, auditLogger *audit.Logger, clock stytch.Clock, txCapable bool) *MembershipService {
	return &MembershipService{
		adapter:   adapter,
		audit:     auditLogger,
		clock:     clock,
		notifier:  emailNotifier{},
		txCapable: txCapable,
	}
}

// Create inserts a new membership. If a duplicate key is hit (same user+workspace),
// the existing membership is returned rather than an error (Req 11.11, 15.1, 15.2).
func (m *MembershipService) Create(ctx context.Context, in CreateMembershipInput) (Membership, error) {
	now := m.clock.Now()
	membership := Membership{
		ID:               uuid.New().String(),
		WorkspaceID:      in.WorkspaceID,
		UserID:           in.UserID,
		Role:             in.Role,
		Status:           MembershipActive,
		AuthorityVersion: 1,
		StytchMemberID:   in.StytchMemberID,
		InvitedBy:        in.InvitedBy,
		InvitationID:     in.InvitationID,
		JoinedAt:         now,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	coll := configs.MI.DB.Collection("memberships")
	_, err := coll.InsertOne(ctx, membership)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Return the existing membership (Req 11.11).
			var existing Membership
			findErr := coll.FindOne(ctx, bson.M{
				"user_id":      in.UserID,
				"workspace_id": in.WorkspaceID,
			}).Decode(&existing)
			if findErr != nil {
				return Membership{}, findErr
			}
			return existing, nil
		}
		return Membership{}, err
	}

	// Write audit entry for membership creation (Req 23.1).
	m.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: in.WorkspaceID,
		ActorUserID: in.InvitedBy,
		ActorKind:   "member",
		Action:      audit.ActionMembershipCreated,
		TargetKind:  "membership",
		TargetID:    membership.ID,
	})

	return membership, nil
}

// ByID returns the membership with the given ID within the workspace, or a
// *MembershipError with Status 404 when it does not exist. Callers that need
// to know a target membership's current role before calling SetStatus (which
// takes targetRole as an explicit parameter rather than looking it up itself)
// use this first.
func (m *MembershipService) ByID(ctx context.Context, workspaceID, membershipID string) (Membership, error) {
	coll := configs.MI.DB.Collection("memberships")
	var membership Membership
	err := coll.FindOne(ctx, bson.M{
		"_id":          membershipID,
		"workspace_id": workspaceID,
	}).Decode(&membership)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return Membership{}, &MembershipError{Status: 404, Code: "membership_not_found", Message: "membership not found"}
		}
		return Membership{}, err
	}
	return membership, nil
}

// List returns all members in a workspace with their display info (Req 13.15).
func (m *MembershipService) List(ctx context.Context, workspaceID string) ([]MemberView, error) {
	coll := configs.MI.DB.Collection("memberships")
	cursor, err := coll.Find(ctx, bson.M{"workspace_id": workspaceID})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var memberships []Membership
	if err := cursor.All(ctx, &memberships); err != nil {
		return nil, err
	}

	userIDs := make([]string, 0, len(memberships))
	for _, mem := range memberships {
		userIDs = append(userIDs, mem.UserID)
	}
	memberUsers, err := users.GetUsersByIDs(userIDs)
	if err != nil {
		return nil, err
	}
	usersByID := make(map[string]users.User, len(memberUsers))
	for _, u := range memberUsers {
		usersByID[u.ID] = u
	}

	views := make([]MemberView, 0, len(memberships))
	for _, mem := range memberships {
		view := MemberView{
			ID:       mem.ID,
			UserID:   mem.UserID,
			Role:     mem.Role,
			Status:   mem.Status,
			JoinedAt: mem.JoinedAt,
		}
		if u, ok := usersByID[mem.UserID]; ok {
			view.FirstName = u.FirstName
			view.LastName = u.LastName
			view.Email = u.Email
		}
		views = append(views, view)
	}
	return views, nil
}

// ChangeRole changes the role of a membership. It is a single findOneAndUpdate
// whose filter encodes the precondition and whose update carries
// $inc: {authority_version: 1} in the SAME write.
// authority_version is never written with $set anywhere (P18 discipline).
// Uses ReturnDocument(Before) so the previous value is available for the audit entry.
//
// Requirements: 13.1, 17.1, 17.4
func (m *MembershipService) ChangeRole(ctx context.Context, workspaceID, membershipID, callerMembershipID string, callerRole Role, newRole Role, actorUserID string) error {
	// Run the guard table validation BEFORE the database write (Req 7.10, 7.11, 7.12, 7.17, 13.8, 14.11).
	if err := m.ValidateRoleChange(ctx, workspaceID, membershipID, callerMembershipID, callerRole, newRole); err != nil {
		return err
	}

	coll := configs.MI.DB.Collection("memberships")
	now := m.clock.Now()

	var before Membership
	err := coll.FindOneAndUpdate(ctx,
		bson.M{
			"_id":          membershipID,
			"workspace_id": workspaceID,
			"status":       MembershipActive,
		},
		bson.M{
			"$set": bson.M{"role": newRole, "updated_at": now},
			"$inc": bson.M{"authority_version": 1},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&before)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &MembershipError{Status: 404, Code: "membership_not_found", Message: "membership not found or not active"}
		}
		return err
	}

	// Invalidate the context cache and publish on the Redis authority channel (Req 17.4).
	m.invalidateAuthority(ctx, before.UserID, workspaceID)

	// Write audit entry (Req 13.10).
	m.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionRoleChanged,
		TargetKind:  "membership",
		TargetID:    membershipID,
		Previous:    map[string]any{"role": string(before.Role)},
		New:         map[string]any{"role": string(newRole)},
	})

	// Best-effort side effects, in this order because the Fundis document is
	// authoritative: it is already written, so neither a Stytch mirror failure
	// (Req 7.15) nor a notification failure may fail this request or roll the
	// document back — that would trade a cosmetic mismatch for an authority
	// mismatch.
	if ws, ok := m.loadWorkspaceForSideEffects(ctx, workspaceID, "role_change"); ok {
		// MirrorRoleToStytch reads Role off the membership it is handed, so it
		// must receive the post-write role, not the pre-image (Req 7.15).
		updated := before
		updated.Role = newRole
		m.MirrorRoleToStytch(ctx, updated, ws)

		// Req 13.9: the affected member alone is notified, naming both roles.
		NotifyRoleChange(ctx, m.notifier, before.UserID, ws.Name, before.Role, newRole)
	}

	return nil
}

// SetStatus changes the status of a membership (suspend, remove, reinstate).
// Same $inc authority_version discipline as ChangeRole.
// Suspend: retains the Stytch member.
// Remove: retains the membership record (sets status to "removed").
// Reinstate: sets status to "active", recreates Stytch member when needed.
//
// Apply zero deletions to the person's users document.
// Retain every companies/job_vacancies/job_applications document they created.
//
// Requirements: 13.2, 13.11, 13.12, 13.13, 13.14, 17.1, 17.4
func (m *MembershipService) SetStatus(ctx context.Context, workspaceID, membershipID, callerMembershipID string, callerRole Role, targetRole Role, newStatus MembershipStatus, actorUserID string) error {
	// Run the guard table validation BEFORE the database write (Req 7.11, 13.7, 14.12, 14.14).
	if err := m.ValidateStatusChange(ctx, workspaceID, membershipID, callerMembershipID, callerRole, targetRole, newStatus); err != nil {
		return err
	}

	coll := configs.MI.DB.Collection("memberships")
	now := m.clock.Now()

	// Build the filter based on the target status transition:
	// - For remove/suspend: membership must be active or suspended
	// - For reinstate (active): membership must be removed or suspended
	var statusFilter bson.M
	switch newStatus {
	case MembershipSuspended:
		// Can suspend an active membership
		statusFilter = bson.M{"status": MembershipActive}
	case MembershipRemoved:
		// Can remove an active or suspended membership
		statusFilter = bson.M{"status": bson.M{"$in": []MembershipStatus{MembershipActive, MembershipSuspended}}}
	case MembershipActive:
		// Reinstate: must be removed or suspended
		statusFilter = bson.M{"status": bson.M{"$in": []MembershipStatus{MembershipRemoved, MembershipSuspended}}}
	}

	filter := bson.M{
		"_id":          membershipID,
		"workspace_id": workspaceID,
	}
	// Merge the status filter into the main filter.
	for k, v := range statusFilter {
		filter[k] = v
	}

	var before Membership
	err := coll.FindOneAndUpdate(ctx,
		filter,
		bson.M{
			"$set": bson.M{"status": newStatus, "updated_at": now},
			"$inc": bson.M{"authority_version": 1},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&before)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &MembershipError{Status: 404, Code: "membership_not_found", Message: "membership not found or status transition not allowed"}
		}
		return err
	}

	// Invalidate the context cache and publish on the Redis authority channel (Req 17.4).
	m.invalidateAuthority(ctx, before.UserID, workspaceID)

	// Write audit entry (Req 13.10).
	m.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionStatusChanged,
		TargetKind:  "membership",
		TargetID:    membershipID,
		Previous:    map[string]any{"status": string(before.Status)},
		New:         map[string]any{"status": string(newStatus)},
	})

	// Best-effort side effects after the authoritative write. Reactivation is
	// deliberately excluded: revoking the sessions of a member who was just
	// reinstated would lock out the person the caller means to restore, and
	// Req 17.3 scopes revocation to `removed` and `suspended` alone.
	if newStatus == MembershipRemoved || newStatus == MembershipSuspended {
		if ws, ok := m.loadWorkspaceForSideEffects(ctx, workspaceID, "status_change"); ok {
			// Req 13.3, 17.3: revoke sessions for both transitions and, for
			// removal only, delete the Stytch member — flagging
			// stytch_member_delete_pending on failure so
			// RetryPendingStytchDeletes picks it up (Req 13.4).
			m.RevokeAndDeleteOnRemoval(ctx, before, ws, newStatus)

			// Req 13.6 names removal alone: the removed person is told their
			// access to the workspace has ended. A suspension is reversible and
			// carries no such requirement.
			if newStatus == MembershipRemoved {
				NotifyMemberRemoval(ctx, m.notifier, before.UserID, ws.Name)
			}
		}
	}

	return nil
}

// ValidateRoleChange checks the eleven-row guard table before a role change.
// Returns a MembershipError if the operation is not allowed.
// This is called by ChangeRole before the database write.
//
// Guard table:
// 1. Admin changing the Owner's role → 403 owner_protected (Req 7.10)
// 2. Admin removing the Owner → 403 owner_protected (Req 7.11)
// 3. Admin granting owner → 403 (Req 7.12)
// 4. Role value outside the four → 400 (Req 7.17)
// 5. Demoting the sole Owner outside TransferOwnership → 409 last_owner_protected (Req 14.11)
// 6. Caller changing their own role → 409 self_role_change_denied (Req 13.8)
// 7. Transfer target not active → 409 (Req 14.5)
// 8. Transfer target's user suspended → 409 (Req 14.6)
// 9. Transfer target is the caller → 400 (Req 14.7)
func (m *MembershipService) ValidateRoleChange(ctx context.Context, workspaceID, membershipID, callerMembershipID string, callerRole Role, newRole Role) error {
	// Guard 4: Role value outside the four valid roles (Req 7.17).
	if !ValidRole(string(newRole)) {
		return &MembershipError{Status: 400, Code: "invalid_role", Message: "role value is not one of the four accepted values"}
	}

	// Guard 6: Caller changing their own role (Req 13.8).
	if callerMembershipID == membershipID {
		return &MembershipError{Status: 409, Code: "self_role_change_denied", Message: "a member cannot change their own role"}
	}

	// Load the target membership to inspect its current role.
	coll := configs.MI.DB.Collection("memberships")
	var target Membership
	err := coll.FindOne(ctx, bson.M{
		"_id":          membershipID,
		"workspace_id": workspaceID,
	}).Decode(&target)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &MembershipError{Status: 404, Code: "membership_not_found", Message: "membership not found"}
		}
		return err
	}

	// Guard 1: Admin changing the Owner's role (Req 7.10).
	if callerRole == RoleAdmin && target.Role == RoleOwner {
		return &MembershipError{Status: 403, Code: "owner_protected", Message: "an admin cannot change the owner's role"}
	}

	// Guard 3: Admin (or any non-owner) granting owner (Req 7.12).
	if newRole == RoleOwner && callerRole != RoleOwner {
		return &MembershipError{Status: 403, Code: "owner_grant_forbidden", Message: "only the owner can grant the owner role"}
	}

	// Guard 5: Demoting the sole Owner outside TransferOwnership (Req 14.11).
	if target.Role == RoleOwner && newRole != RoleOwner {
		count, countErr := m.countActiveOwners(ctx, workspaceID)
		if countErr != nil {
			return countErr
		}
		if count <= 1 {
			return &MembershipError{Status: 409, Code: "last_owner_protected", Message: "cannot demote the sole owner; use ownership transfer instead"}
		}
	}

	return nil
}

// ValidateStatusChange checks guards before a status change.
//
// Guard table:
// 1. Suspending or removing the sole Owner → 409 last_owner_protected (Req 14.12)
// 2. Owner removing their own membership → 409 owner_must_transfer_first (Req 13.7)
// 3. Sole member removing their membership → 409, direct to deletion flow (Req 14.14)
// 4. Transfer target not active → 409 (Req 14.5)
// 5. Transfer target's user suspended → 409 (Req 14.6)
// 6. Transfer target is the caller → 400 (Req 14.7)
func (m *MembershipService) ValidateStatusChange(ctx context.Context, workspaceID, membershipID, callerMembershipID string, callerRole Role, targetRole Role, newStatus MembershipStatus) error {
	// Validate the target status value.
	switch newStatus {
	case MembershipActive, MembershipSuspended, MembershipRemoved:
		// valid
	default:
		return &MembershipError{Status: 400, Code: "invalid_status", Message: "invalid membership status value"}
	}

	// Guard: Admin suspending or removing the Owner (Req 7.11, covered by guard table row 2).
	if callerRole == RoleAdmin && targetRole == RoleOwner && (newStatus == MembershipSuspended || newStatus == MembershipRemoved) {
		return &MembershipError{Status: 403, Code: "owner_protected", Message: "an admin cannot suspend or remove the owner"}
	}

	// Guard: Owner removing their own membership (Req 13.7).
	if callerMembershipID == membershipID && targetRole == RoleOwner && newStatus == MembershipRemoved {
		return &MembershipError{Status: 409, Code: "owner_must_transfer_first", Message: "the owner must transfer ownership before removing their own membership"}
	}

	// Guard: Suspending or removing the sole Owner (Req 14.12).
	if targetRole == RoleOwner && (newStatus == MembershipSuspended || newStatus == MembershipRemoved) {
		count, countErr := m.countActiveOwners(ctx, workspaceID)
		if countErr != nil {
			return countErr
		}
		if count <= 1 {
			return &MembershipError{Status: 409, Code: "last_owner_protected", Message: "cannot suspend or remove the sole owner"}
		}
	}

	// Guard: Sole member removing their membership → direct to deletion (Req 14.14).
	if newStatus == MembershipRemoved {
		activeCount, countErr := m.countActiveMembers(ctx, workspaceID)
		if countErr != nil {
			return countErr
		}
		if activeCount <= 1 && callerMembershipID == membershipID {
			return &MembershipError{Status: 409, Code: "sole_member_must_delete_workspace", Message: "the sole member cannot self-remove; use the workspace deletion flow instead"}
		}
	}

	return nil
}

// ValidateTransferTarget checks guards specific to ownership transfer targets.
// Returns a MembershipError if the target is invalid.
//
// Guard table rows for transfer:
// - Transfer target not active → 409 (Req 14.5)
// - Transfer target's user suspended → 409 (Req 14.6)
// - Transfer target is the caller → 400 (Req 14.7)
func (m *MembershipService) ValidateTransferTarget(ctx context.Context, targetMembership Membership, callerMembershipID string) error {
	// Guard: Transfer target is the caller (Req 14.7).
	if targetMembership.ID == callerMembershipID {
		return &MembershipError{Status: 400, Code: "transfer_to_self", Message: "cannot transfer ownership to yourself"}
	}

	// Guard: Transfer target not active (Req 14.5).
	if targetMembership.Status != MembershipActive {
		return &MembershipError{Status: 409, Code: "transfer_target_not_active", Message: "ownership transfer target must have active membership status"}
	}

	// Guard: Transfer target's user suspended (Req 14.6).
	user, err := users.GetUserByID(ctx, targetMembership.UserID)
	if err != nil {
		return err
	}
	if user.Status == "suspended" {
		return &MembershipError{Status: 409, Code: "transfer_target_user_suspended", Message: "ownership transfer target's user account is suspended"}
	}

	return nil
}

// TransferOwnership atomically swaps the owner role between the caller and
// the target membership using a MongoDB multi-document transaction.
//
// Returns 503 transactions_unavailable when m.txCapable is false rather than
// performing a silent best-effort swap (unreachable defence in depth).
//
// Inside WithTransaction:
// 1. Demote caller: {_id: caller, role: owner, status: active} → admin, $inc version
// 2. Promote target: {_id: target, status: active, role: {$ne: owner}} → owner, $inc version
// Both must match exactly one document or the transaction aborts.
//
// Neither ordering works without a transaction:
// - promote-then-demote is rejected by uniq_active_owner_per_workspace
// - demote-then-promote leaves a zero-Owner window (Req 14.1 forbids)
//
// Requirements: 14.3, 14.4, 14.8
func (m *MembershipService) TransferOwnership(ctx context.Context, workspaceID, callerMembershipID, targetMembershipID, actorUserID string) error {
	// 1. Load the target membership and run ValidateTransferTarget.
	coll := configs.MI.DB.Collection("memberships")
	var target Membership
	err := coll.FindOne(ctx, bson.M{
		"_id":          targetMembershipID,
		"workspace_id": workspaceID,
	}).Decode(&target)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &MembershipError{Status: 404, Code: "membership_not_found", Message: "target membership not found"}
		}
		return err
	}

	if err := m.ValidateTransferTarget(ctx, target, callerMembershipID); err != nil {
		return err
	}

	// 2. Check m.txCapable — if false, return 503 transactions_unavailable.
	// This is unreachable defence in depth: startup fails when employer Stytch auth
	// is enabled and the deployment does not support transactions.
	if !m.txCapable {
		return &MembershipError{Status: 503, Code: "transactions_unavailable", Message: "ownership transfer requires transaction support"}
	}

	// 3. Execute the ownership swap inside a MongoDB transaction.
	now := m.clock.Now()

	err = configs.MI.Client.UseSession(ctx, func(sctx mongo.SessionContext) error {
		_, txErr := sctx.WithTransaction(sctx, func(sctx mongo.SessionContext) (any, error) {
			membersColl := sctx.Client().Database(configs.MI.DB.Name()).Collection("memberships")

			// a. Demote caller: filter requires the caller is currently the active owner.
			demoteResult := membersColl.FindOneAndUpdate(sctx,
				bson.M{
					"_id":          callerMembershipID,
					"workspace_id": workspaceID,
					"role":         RoleOwner,
					"status":       MembershipActive,
				},
				bson.M{
					"$set": bson.M{"role": RoleAdmin, "updated_at": now},
					"$inc": bson.M{"authority_version": 1},
				},
				options.FindOneAndUpdate().SetReturnDocument(options.After),
			)
			if demoteResult.Err() != nil {
				if demoteResult.Err() == mongo.ErrNoDocuments {
					return nil, &MembershipError{Status: 409, Code: "transfer_precondition_failed", Message: "caller is not the active owner of this workspace"}
				}
				return nil, demoteResult.Err()
			}

			// b. Promote target: filter requires the target is active and NOT already an owner.
			promoteResult := membersColl.FindOneAndUpdate(sctx,
				bson.M{
					"_id":          targetMembershipID,
					"workspace_id": workspaceID,
					"status":       MembershipActive,
					"role":         bson.M{"$ne": RoleOwner},
				},
				bson.M{
					"$set": bson.M{"role": RoleOwner, "updated_at": now},
					"$inc": bson.M{"authority_version": 1},
				},
				options.FindOneAndUpdate().SetReturnDocument(options.After),
			)
			if promoteResult.Err() != nil {
				if promoteResult.Err() == mongo.ErrNoDocuments {
					return nil, &MembershipError{Status: 409, Code: "transfer_precondition_failed", Message: "target membership is not eligible for ownership promotion"}
				}
				return nil, promoteResult.Err()
			}

			return nil, nil
		})
		return txErr
	})
	if err != nil {
		return err
	}

	// 5. Invalidate authority for both users.
	// Load the caller membership to get its UserID for cache invalidation. A
	// failure here must not be silently swallowed: skipping invalidation
	// leaves the caller's stale cached session holding Owner permissions
	// after they were demoted to Admin — a privilege-escalation window.
	var caller Membership
	callerLoaded := true
	if err := coll.FindOne(ctx, bson.M{"_id": callerMembershipID}).Decode(&caller); err != nil {
		callerLoaded = false
		slog.ErrorContext(ctx, "transfer: failed to load caller for cache invalidation", "membership_id", callerMembershipID, "error", err)
	} else {
		m.invalidateAuthority(ctx, caller.UserID, workspaceID)
	}
	m.invalidateAuthority(ctx, target.UserID, workspaceID)

	// 6. Write audit entry for ownership transfer (Req 14.10).
	m.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionOwnershipTransferred,
		TargetKind:  "membership",
		TargetID:    targetMembershipID,
		Previous:    map[string]any{"owner_membership_id": callerMembershipID, "owner_role": "owner"},
		New:         map[string]any{"owner_membership_id": targetMembershipID, "new_owner_role": "owner", "previous_owner_role": "admin"},
	})

	// 7. Best-effort side effects after the committed swap. A transfer changes
	// two Workspace_Roles, so Req 7.15's mirror applies to both Stytch members.
	if ws, ok := m.loadWorkspaceForSideEffects(ctx, workspaceID, "ownership_transfer"); ok {
		if callerLoaded {
			demoted := caller
			demoted.Role = RoleAdmin
			m.MirrorRoleToStytch(ctx, demoted, ws)
		}
		promoted := target
		promoted.Role = RoleOwner
		m.MirrorRoleToStytch(ctx, promoted, ws)

		// Req 14.9: both the previous and the new owner are emailed. Without
		// the caller's user ID there is no previous-owner address to send to,
		// and the failure is already logged above.
		if callerLoaded {
			NotifyOwnershipTransfer(ctx, m.notifier, caller.UserID, target.UserID, ws.Name)
		}
	}

	return nil
}

// loadWorkspaceForSideEffects reads the Workspace document the Stytch mirror
// and the notification emails both need — the former for
// StytchOrganizationID, the latter for the workspace name. It is called only
// after the authoritative membership write has already succeeded, so a failure
// here cannot fail the operation: it is logged and reported as false, and the
// caller skips its best-effort side effects.
func (m *MembershipService) loadWorkspaceForSideEffects(ctx context.Context, workspaceID, operation string) (Workspace, bool) {
	var ws Workspace
	err := configs.MI.DB.Collection("workspaces").FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&ws)
	if err != nil {
		slog.WarnContext(ctx, "membership: failed to load workspace for post-write side effects",
			"workspace_id", workspaceID,
			"operation", operation,
			"error", err.Error(),
		)
		return Workspace{}, false
	}
	return ws, true
}

// countActiveOwners counts active memberships with role "owner" in a workspace.
// Used to enforce the sole-owner protection rules (Req 14.11, 14.12).
func (m *MembershipService) countActiveOwners(ctx context.Context, workspaceID string) (int64, error) {
	coll := configs.MI.DB.Collection("memberships")
	return coll.CountDocuments(ctx, bson.M{
		"workspace_id": workspaceID,
		"role":         RoleOwner,
		"status":       MembershipActive,
	})
}

// countActiveMembers counts all active memberships in a workspace.
// Used to enforce the sole-member deletion-flow guard (Req 14.14).
func (m *MembershipService) countActiveMembers(ctx context.Context, workspaceID string) (int64, error) {
	coll := configs.MI.DB.Collection("memberships")
	return coll.CountDocuments(ctx, bson.M{
		"workspace_id": workspaceID,
		"status":       MembershipActive,
	})
}

// AdminTransferOwnership transfers ownership from a platform-suspended user to
// a named active membership. Called from POST /admin/workspaces/:workspaceID/ownership
// behind middleware.IsAdmin. (Req 14.13)
//
// This retains the owner membership of the platform-suspended user (doesn't remove it)
// and promotes the target to owner while demoting the previous owner to admin.
func (m *MembershipService) AdminTransferOwnership(ctx context.Context, workspaceID, targetMembershipID, adminUserID string) error {
	coll := configs.MI.DB.Collection("memberships")

	// 1. Find the current owner membership in the workspace.
	var currentOwner Membership
	err := coll.FindOne(ctx, bson.M{
		"workspace_id": workspaceID,
		"role":         RoleOwner,
		"status":       MembershipActive,
	}).Decode(&currentOwner)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &MembershipError{Status: 404, Code: "no_active_owner", Message: "workspace has no active owner membership"}
		}
		return err
	}

	// Verify the current owner's user is actually platform-suspended (Req
	// 14.13). Without this check, an admin could invoke this on any workspace
	// with an active, non-suspended owner — a backdoor around the normal
	// TransferOwnership guards.
	currentOwnerUser, err := users.GetUserByID(ctx, currentOwner.UserID)
	if err != nil {
		return err
	}
	if currentOwnerUser.Status != "suspended" {
		return &MembershipError{Status: 409, Code: "owner_not_suspended", Message: "admin ownership transfer is only allowed for platform-suspended owners"}
	}

	// 2. Load the target membership, validate it's active and its user isn't suspended.
	var target Membership
	err = coll.FindOne(ctx, bson.M{
		"_id":          targetMembershipID,
		"workspace_id": workspaceID,
	}).Decode(&target)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &MembershipError{Status: 404, Code: "membership_not_found", Message: "target membership not found in this workspace"}
		}
		return err
	}

	// Guard: transfer target is the current owner (mirrors the self-transfer
	// guard in ValidateTransferTarget, Req 14.7). Without it, this would
	// demote and promote the same membership in one transaction.
	if target.ID == currentOwner.ID {
		return &MembershipError{Status: 400, Code: "transfer_to_self", Message: "cannot transfer ownership to the current owner"}
	}

	if target.Status != MembershipActive {
		return &MembershipError{Status: 409, Code: "transfer_target_not_active", Message: "ownership transfer target must have active membership status"}
	}

	user, err := users.GetUserByID(ctx, target.UserID)
	if err != nil {
		return err
	}
	if user.Status == "suspended" {
		return &MembershipError{Status: 409, Code: "transfer_target_user_suspended", Message: "ownership transfer target's user account is suspended"}
	}

	// 3. Check transaction capability.
	if !m.txCapable {
		return &MembershipError{Status: 503, Code: "transactions_unavailable", Message: "ownership transfer requires transaction support"}
	}

	// 4. Execute the transaction: demote current owner → admin, promote target → owner.
	now := m.clock.Now()

	err = configs.MI.Client.UseSession(ctx, func(sctx mongo.SessionContext) error {
		_, txErr := sctx.WithTransaction(sctx, func(sctx mongo.SessionContext) (any, error) {
			membersColl := sctx.Client().Database(configs.MI.DB.Name()).Collection("memberships")

			// Demote current owner to admin.
			demoteResult := membersColl.FindOneAndUpdate(sctx,
				bson.M{
					"_id":          currentOwner.ID,
					"workspace_id": workspaceID,
					"role":         RoleOwner,
					"status":       MembershipActive,
				},
				bson.M{
					"$set": bson.M{"role": RoleAdmin, "updated_at": now},
					"$inc": bson.M{"authority_version": 1},
				},
				options.FindOneAndUpdate().SetReturnDocument(options.After),
			)
			if demoteResult.Err() != nil {
				if demoteResult.Err() == mongo.ErrNoDocuments {
					return nil, &MembershipError{Status: 409, Code: "transfer_precondition_failed", Message: "current owner state changed during transfer"}
				}
				return nil, demoteResult.Err()
			}

			// Promote target to owner.
			promoteResult := membersColl.FindOneAndUpdate(sctx,
				bson.M{
					"_id":          targetMembershipID,
					"workspace_id": workspaceID,
					"status":       MembershipActive,
					"role":         bson.M{"$ne": RoleOwner},
				},
				bson.M{
					"$set": bson.M{"role": RoleOwner, "updated_at": now},
					"$inc": bson.M{"authority_version": 1},
				},
				options.FindOneAndUpdate().SetReturnDocument(options.After),
			)
			if promoteResult.Err() != nil {
				if promoteResult.Err() == mongo.ErrNoDocuments {
					return nil, &MembershipError{Status: 409, Code: "transfer_precondition_failed", Message: "target membership is not eligible for ownership promotion"}
				}
				return nil, promoteResult.Err()
			}

			return nil, nil
		})
		return txErr
	})
	if err != nil {
		return err
	}

	// 5. Invalidate authority for both users.
	m.invalidateAuthority(ctx, currentOwner.UserID, workspaceID)
	m.invalidateAuthority(ctx, target.UserID, workspaceID)

	// 6. Write audit entry with ActorKind: "platform_admin" (Req 23.9).
	m.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: adminUserID,
		ActorKind:   "platform_admin",
		Action:      audit.ActionOwnershipTransferred,
		TargetKind:  "membership",
		TargetID:    targetMembershipID,
		Previous:    map[string]any{"owner_membership_id": currentOwner.ID, "owner_role": "owner"},
		New:         map[string]any{"owner_membership_id": targetMembershipID, "new_owner_role": "owner", "previous_owner_role": "admin"},
	})

	// 7. Best-effort side effects after the committed swap.
	if ws, ok := m.loadWorkspaceForSideEffects(ctx, workspaceID, "admin_ownership_transfer"); ok {
		// Req 7.15: both roles changed, so both Stytch members are mirrored.
		demoted := currentOwner
		demoted.Role = RoleAdmin
		m.MirrorRoleToStytch(ctx, demoted, ws)
		promoted := target
		promoted.Role = RoleOwner
		m.MirrorRoleToStytch(ctx, promoted, ws)

		// Req 14.9 applies to every completed transfer, including this
		// administrator-driven one (Req 14.13).
		NotifyOwnershipTransfer(ctx, m.notifier, currentOwner.UserID, target.UserID, ws.Name)
	}

	return nil
}

// DetectZeroOwnerWorkspaces returns workspace IDs that have zero active owner
// memberships. Used by the orphan report (Req 14.15).
func (m *MembershipService) DetectZeroOwnerWorkspaces(ctx context.Context) ([]string, error) {
	coll := configs.MI.DB.Collection("memberships")

	// 1. Aggregate memberships: group by workspace_id where role=owner and status=active,
	//    count per workspace.
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"role": RoleOwner, "status": MembershipActive}}},
		{{Key: "$group", Value: bson.M{"_id": "$workspace_id", "count": bson.M{"$sum": 1}}}},
	}

	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	// Collect workspace IDs that have at least one active owner.
	hasOwner := make(map[string]struct{})
	for cursor.Next(ctx) {
		var result struct {
			ID    string `bson:"_id"`
			Count int    `bson:"count"`
		}
		if err := cursor.Decode(&result); err != nil {
			return nil, err
		}
		hasOwner[result.ID] = struct{}{}
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}

	// 2. Find all workspace IDs NOT in that set (workspaces with zero active owners).
	workspacesColl := configs.MI.DB.Collection("workspaces")
	// Only check non-deleted workspaces.
	wsCursor, err := workspacesColl.Find(ctx, bson.M{
		"deletion_state": bson.M{"$nin": []DeletionState{DeletionDeleted}},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = wsCursor.Close(ctx) }()

	var orphaned []string
	for wsCursor.Next(ctx) {
		var ws Workspace
		if err := wsCursor.Decode(&ws); err != nil {
			return nil, err
		}
		if _, ok := hasOwner[ws.ID]; !ok {
			orphaned = append(orphaned, ws.ID)
		}
	}
	if err := wsCursor.Err(); err != nil {
		return nil, err
	}

	// 3. Log one record for each (Req 14.15). Warn, not Error: finding zero-
	// owner workspaces is the expected outcome of this routine admin report,
	// not a failure — Error-level here would pollute on-call alerts.
	for _, wsID := range orphaned {
		slog.WarnContext(ctx, "membership: workspace has zero active owner memberships",
			"workspace_id", wsID,
		)
	}

	return orphaned, nil
}

// SetStatusFromWebhook applies a Membership_Status change reported by a
// Stytch webhook event (Req 19.7). Unlike SetStatus, this is system-initiated
// — there is no caller Membership or Workspace_Role to run the guard table
// against, so it writes directly. eventTS guards against a late-arriving
// stale event: the update matches zero documents when a newer event (or a
// newer Fundis-initiated change) has already touched this membership, so an
// out-of-order webhook can never overwrite fresher state (Req 19.10).
//
// This path deliberately performs no Stytch side effects, unlike SetStatus.
// Stytch is the originator of the change, so mirroring it back would be a
// feedback loop. Concretely, the only event that reaches here with `removed` is
// member.delete (see WebhookConsumer.handleMemberDeleted): the Stytch_Member is
// already gone, so DeleteMember would return 404, set
// stytch_member_delete_pending, and hand RetryPendingStytchDeletes 24 hours of
// futile retries against an identity that no longer exists. Session revocation
// is moot for the same reason — a deleted member holds no live sessions. Nor is
// a notification sent: Req 13.6 is scoped to "WHEN a caller removes a
// Membership", and there is no caller here.
func (m *MembershipService) SetStatusFromWebhook(ctx context.Context, membershipID string, newStatus MembershipStatus, eventTS time.Time) error {
	coll := configs.MI.DB.Collection("memberships")
	now := m.clock.Now()

	var before Membership
	err := coll.FindOneAndUpdate(ctx,
		bson.M{
			"_id": membershipID,
			"$or": bson.A{
				bson.M{"last_webhook_event_at": bson.M{"$exists": false}},
				bson.M{"last_webhook_event_at": bson.M{"$lt": eventTS}},
			},
		},
		bson.M{
			"$set": bson.M{
				"status":                newStatus,
				"last_webhook_event_at": eventTS,
				"updated_at":            now,
			},
			"$inc": bson.M{"authority_version": 1},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&before)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Either the membership doesn't exist, or a fresher event/change
			// already applied — both are a no-op, not a failure.
			return nil
		}
		return err
	}

	m.invalidateAuthority(ctx, before.UserID, before.WorkspaceID)

	m.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: before.WorkspaceID,
		ActorUserID: before.UserID,
		ActorKind:   "system",
		Action:      audit.ActionStatusChanged,
		TargetKind:  "membership",
		TargetID:    membershipID,
		Previous:    map[string]any{"status": string(before.Status)},
		New:         map[string]any{"status": string(newStatus)},
	})
	return nil
}

// pickReplacementMemberID selects an already-existing Stytch member to adopt
// as the owner's replacement, or "" when none qualifies and one must be
// created. deletedMemberID — the member the webhook reported deleted — is
// skipped: Stytch may still return it from a search briefly after deletion,
// and adopting it would point the membership back at the dead member.
func pickReplacementMemberID(existing []stytch.Member, deletedMemberID string) string {
	for _, candidate := range existing {
		if candidate.MemberID != "" && candidate.MemberID != deletedMemberID {
			return candidate.MemberID
		}
	}
	return ""
}

// RecreateOwnerMember handles a Stytch webhook reporting deletion of the
// Stytch_Member behind an `owner` Membership (Req 19.8). The Membership is
// retained — an Owner Membership must never be removed as a side effect of a
// webhook — and a replacement Stytch_Member is created so the Workspace's
// Stytch_Organization has an Owner-equivalent member again. Every failure is
// returned so the webhook consumer can drop its deduplication record and let
// Stytch redeliver: a silently failed recreation leaves the Owner Membership
// pointing at a deleted Stytch_Member, which no later event repairs.
func (m *MembershipService) RecreateOwnerMember(ctx context.Context, membership Membership, ws Workspace) error {
	if ws.StytchOrganizationID == "" {
		return nil
	}

	ownerUser, err := users.GetUserByID(ctx, membership.UserID)
	if err != nil {
		return fmt.Errorf("webhook: load owner user for stytch member recreation: %w", err)
	}

	stytchCtx := stytch.ContextWithWorkspaceID(ctx, membership.WorkspaceID)

	// Because every failure below is returned, a failure after CreateMember
	// succeeded (notably the membership update) makes Stytch redeliver the
	// event and run this again. Searching first makes that path idempotent:
	// without it the retry would create a second owner member in the
	// organization and the membership would point at only one of them.
	var newMemberID string
	err = m.adapter.Do(stytchCtx, "SearchMembersByEmail", false, func(attemptCtx context.Context) error {
		existing, searchErr := m.adapter.Client().SearchMembersByEmail(attemptCtx, ws.StytchOrganizationID, ownerUser.Email)
		if searchErr != nil {
			return searchErr
		}
		newMemberID = pickReplacementMemberID(existing, membership.StytchMemberID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("webhook: search for existing owner stytch member: %w", err)
	}

	if newMemberID == "" {
		err = m.adapter.Do(stytchCtx, "CreateMember", true, func(attemptCtx context.Context) error {
			member, createErr := m.adapter.Client().CreateMember(attemptCtx, stytch.CreateMemberInput{
				OrganizationID: ws.StytchOrganizationID,
				Email:          ownerUser.Email,
				Name:           ownerUser.FirstName + " " + ownerUser.LastName,
				Roles:          []string{string(membership.Role)},
			})
			if createErr != nil {
				return createErr
			}
			newMemberID = member.MemberID
			return nil
		})
		if err != nil {
			return fmt.Errorf("webhook: recreate owner stytch member: %w", err)
		}
	}

	coll := configs.MI.DB.Collection("memberships")
	if _, updateErr := coll.UpdateOne(ctx,
		bson.M{"_id": membership.ID},
		bson.M{"$set": bson.M{"stytch_member_id": newMemberID, "updated_at": m.clock.Now()}},
	); updateErr != nil {
		return fmt.Errorf("webhook: record recreated owner stytch member id: %w", updateErr)
	}
	return nil
}

// invalidateAuthority publishes an authority invalidation message on the Redis
// channel "fundis:workspace:authority" so that all instances can purge their
// context cache entry for this (userID, workspaceID) pair. When configs.RDB is
// nil (Redis not configured), the publish is skipped and the 30-second TTL is
// the only staleness bound — a documented degradation (Req 17.4).
func (m *MembershipService) invalidateAuthority(ctx context.Context, userID, workspaceID string) {
	if configs.RDB == nil {
		return
	}

	payload := map[string]string{
		"user_id":      userID,
		"workspace_id": workspaceID,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.ErrorContext(ctx, "membership: failed to marshal authority invalidation payload",
			"user_id", userID,
			"workspace_id", workspaceID,
			"error", err.Error(),
		)
		return
	}

	if err := configs.RDB.Publish(ctx, "fundis:workspace:authority", data).Err(); err != nil {
		slog.WarnContext(ctx, "membership: failed to publish authority invalidation",
			"user_id", userID,
			"workspace_id", workspaceID,
			"error", err.Error(),
		)
	}
}
