package invitations

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/identity"
	"backend/ratelimit"
	"backend/stytch"
	"backend/users"
	"backend/workspaces"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PublicView is the information shown to an invited person BEFORE authentication.
// Returned by Resolve so the frontend can display "You've been invited to <workspace>
// as <role> by <inviter>" without requiring sign-in. (Req 11.1)
type PublicView struct {
	WorkspaceName string `json:"workspace_name"`
	Role          string `json:"role"`
	InviterName   string `json:"inviter_name"`
	InvitedEmail  string `json:"invited_email"` // masked
}

// InvitationError is a structured error returned by Service operations
// when a precondition or validation check fails.
type InvitationError struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Field   string         `json:"field,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *InvitationError) Error() string { return e.Message }

// CreateInput carries the parameters for creating an invitation.
type CreateInput struct {
	WorkspaceID string
	Email       string
	Role        string // "admin", "hr", or "finance"
	InvitedBy   string // user ID of the inviter
}

// CreateResult is the response from Create.
type CreateResult struct {
	Invitation Invitation
	Token      string // plaintext token for the email (never stored)
	Updated    bool   // true if an existing pending invitation was updated
}

// AcceptInput carries the parameters for accepting an invitation.
type AcceptInput struct {
	Token              string // plaintext invitation token
	AuthenticatedEmail string // the email of the authenticated user
	UserID             string // the user ID of the authenticated user (may be empty if new)
	StytchMemberID     string // the Stytch member ID from the new org membership
}

// AcceptResult is the response from Accept.
type AcceptResult struct {
	MembershipID  string `json:"membership_id"`
	WorkspaceID   string `json:"workspace_id"`
	Role          string `json:"role"`
	AlreadyMember bool   `json:"already_member,omitempty"` // true if duplicate key
}

// Service manages invitation lifecycle.
type Service struct {
	rate       ratelimit.RateLimiter
	audit      *audit.Logger
	clock      stytch.Clock
	membership *workspaces.MembershipService
}

// NewService constructs an invitation Service.
func NewService(rate ratelimit.RateLimiter, auditLogger *audit.Logger, clock stytch.Clock, membership *workspaces.MembershipService) *Service {
	return &Service{rate: rate, audit: auditLogger, clock: clock, membership: membership}
}

// validInvitableRoles is the closed set of roles that may be offered in an invitation.
var validInvitableRoles = map[string]bool{
	string(workspaces.RoleAdmin):   true,
	string(workspaces.RoleHR):      true,
	string(workspaces.RoleFinance): true,
}

// Create issues or updates an invitation.
//
// Implementation:
//  1. Check 20-per-workspace-per-hour limit -> 429 invite_rate_limited (Req 10.12)
//  2. Count pending invitations -> 409 pending_invite_limit at 100 (Req 10.13)
//  3. Normalize email (Req 10.2)
//  4. Reject role "owner" -> 400 owner_not_invitable (Req 10.3)
//  5. Reject unparseable email -> 400 naming "email" field (Req 10.4)
//  6. Check if the email already has an active membership -> 409 already_a_member (Req 10.5)
//  7. Reject if workspace deletion is scheduled or complete -> 409 (Req 10.18)
//  8. Upsert on uniq_pending_invitation_workspace_email:
//     - If existing pending row: update role, refresh expiry -> 200 invitation_updated (Req 10.6)
//     - If new: insert with token_hash, expires_at=now+7d -> 201
//  9. Record inviter, workspace, email, role, issuance, expiry (Req 10.10)
//
// Requirements: 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.9, 10.10, 10.12, 10.13, 10.14, 10.15, 10.16, 10.18
func (s *Service) Create(ctx context.Context, in CreateInput) (CreateResult, error) {
	// 1. Rate limit: 20 invitations per workspace per hour (Req 10.12).
	decision := s.rate.Allow(ctx, ratelimit.LimitInviteCreate, in.WorkspaceID)
	if !decision.Allowed {
		return CreateResult{}, &InvitationError{
			Status:  429,
			Code:    "invite_rate_limited",
			Message: "invitation creation rate limit exceeded for this workspace",
			Details: map[string]any{
				"retry_after_seconds": int(decision.RetryAfter.Seconds()),
			},
		}
	}

	// 2. Count pending invitations: cap at 100 (Req 10.13).
	coll := configs.MI.DB.Collection("invitations")
	pendingCount, err := coll.CountDocuments(ctx, bson.M{
		"workspace_id": in.WorkspaceID,
		"status":       StatusPending,
	})
	if err != nil {
		return CreateResult{}, fmt.Errorf("invitations.Create: counting pending: %w", err)
	}
	if pendingCount >= 100 {
		return CreateResult{}, &InvitationError{
			Status:  409,
			Code:    "pending_invite_limit",
			Message: "workspace has reached the maximum of 100 pending invitations",
		}
	}

	// 3. Normalize email (Req 10.2).
	normalized := identity.NormalizeEmail(in.Email)

	// 4. Reject role "owner" (Req 10.3).
	if in.Role == string(workspaces.RoleOwner) {
		return CreateResult{}, &InvitationError{
			Status:  400,
			Code:    "owner_not_invitable",
			Message: "the owner role cannot be granted by invitation",
		}
	}

	// Validate the role is one of the invitable set.
	if !validInvitableRoles[in.Role] {
		return CreateResult{}, &InvitationError{
			Status:  400,
			Code:    "invalid_role",
			Message: fmt.Sprintf("role %q is not a valid invitation role", in.Role),
			Field:   "role",
		}
	}

	// 5. Validate email format (Req 10.4).
	if _, parseErr := mail.ParseAddress(normalized); parseErr != nil {
		return CreateResult{}, &InvitationError{
			Status:  400,
			Code:    "invalid_email",
			Message: "the provided email address is not valid",
			Field:   "email",
		}
	}

	// 6. Check if the email already has an active membership in this workspace (Req 10.5).
	membershipsColl := configs.MI.DB.Collection("memberships")
	usersColl := configs.MI.DB.Collection("users")

	// Find users with this normalized email.
	usersCursor, err := usersColl.Find(ctx, bson.M{"email_normalized": normalized})
	if err != nil {
		return CreateResult{}, fmt.Errorf("invitations.Create: finding users by email: %w", err)
	}
	defer func() { _ = usersCursor.Close(ctx) }()

	type userDoc struct {
		ID string `bson:"_id"`
	}
	var userIDs []string
	for usersCursor.Next(ctx) {
		var u userDoc
		if decErr := usersCursor.Decode(&u); decErr == nil {
			userIDs = append(userIDs, u.ID)
		}
	}

	if len(userIDs) > 0 {
		// Check if any of these users has an active membership in the workspace.
		var existingMembership workspaces.Membership
		findErr := membershipsColl.FindOne(ctx, bson.M{
			"workspace_id": in.WorkspaceID,
			"user_id":      bson.M{"$in": userIDs},
			"status":       workspaces.MembershipActive,
		}).Decode(&existingMembership)
		if findErr == nil {
			// Active member found.
			return CreateResult{}, &InvitationError{
				Status:  409,
				Code:    "already_a_member",
				Message: "this email address already has an active membership in this workspace",
				Details: map[string]any{
					"existing_role": string(existingMembership.Role),
				},
			}
		}
		if findErr != mongo.ErrNoDocuments {
			return CreateResult{}, fmt.Errorf("invitations.Create: checking memberships: %w", findErr)
		}
	}

	// 7. Reject if workspace deletion is scheduled or complete (Req 10.18).
	workspacesColl := configs.MI.DB.Collection("workspaces")
	var ws workspaces.Workspace
	wsErr := workspacesColl.FindOne(ctx, bson.M{"_id": in.WorkspaceID}).Decode(&ws)
	if wsErr != nil {
		if wsErr == mongo.ErrNoDocuments {
			return CreateResult{}, &InvitationError{
				Status:  404,
				Code:    "workspace_not_found",
				Message: "workspace not found",
			}
		}
		return CreateResult{}, fmt.Errorf("invitations.Create: finding workspace: %w", wsErr)
	}
	if ws.DeletionState == workspaces.DeletionScheduled || ws.DeletionState == workspaces.DeletionDeleted {
		return CreateResult{}, &InvitationError{
			Status:  409,
			Code:    "workspace_deletion_scheduled",
			Message: "cannot create invitations for a workspace whose deletion is scheduled or complete",
		}
	}

	// 8. Upsert on uniq_pending_invitation_workspace_email (Req 10.6).
	now := s.clock.Now()
	expiresAt := now.Add(168 * time.Hour) // 7 days (Req 10.9)

	// Generate a new token for the invitation.
	plaintext, tokenHash, tokenErr := newToken()
	if tokenErr != nil {
		return CreateResult{}, fmt.Errorf("invitations.Create: generating token: %w", tokenErr)
	}

	invitationID := uuid.New().String()

	filter := bson.M{
		"workspace_id": in.WorkspaceID,
		"email":        normalized,
		"status":       string(StatusPending),
	}

	update := bson.M{
		"$set": bson.M{
			"role":           in.Role,
			"token_hash":     tokenHash,
			"invited_by":     in.InvitedBy,
			"expires_at":     expiresAt,
			"delivery_state": "pending",
			"updated_at":     now,
		},
		"$setOnInsert": bson.M{
			"_id":          invitationID,
			"workspace_id": in.WorkspaceID,
			"email":        normalized,
			"status":       string(StatusPending),
			"issued_at":    now,
			"created_at":   now,
			"resend_count": 0,
		},
	}

	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var result Invitation
	upsertErr := coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if upsertErr != nil {
		return CreateResult{}, fmt.Errorf("invitations.Create: upserting invitation: %w", upsertErr)
	}

	// Determine if this was an update (existing document) or a new insert.
	// If the ID differs from what we generated, it was an existing document.
	updated := result.ID != invitationID

	// 9. Write audit entry (Req 10.10).
	s.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: in.WorkspaceID,
		ActorUserID: in.InvitedBy,
		ActorKind:   "member",
		Action:      audit.ActionInvitationCreated,
		TargetKind:  "invitation",
		TargetID:    result.ID,
		New: map[string]any{
			"email":      normalized,
			"role":       in.Role,
			"expires_at": expiresAt.Format(time.RFC3339),
		},
	})

	return CreateResult{
		Invitation: result,
		Token:      plaintext,
		Updated:    updated,
	}, nil
}

// Resend mints a fresh token, invalidates the previous one, resets the expiry
// to 7 days out, sends one email, and is capped at 3 per invitation per hour (Req 12.4).
// Rejects an invitation already accepted, declined, or revoked with 409 (Req 12.9).
//
// Requirements: 12.3, 12.4, 12.9, 12.11
func (s *Service) Resend(ctx context.Context, workspaceID, invitationID, actorUserID string) (string, error) {
	coll := configs.MI.DB.Collection("invitations")

	// 1. Find the invitation by ID and workspace_id.
	var inv Invitation
	err := coll.FindOne(ctx, bson.M{
		"_id":          invitationID,
		"workspace_id": workspaceID,
	}).Decode(&inv)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", &InvitationError{
				Status:  404,
				Code:    "invitation_not_found",
				Message: "invitation not found",
			}
		}
		return "", fmt.Errorf("invitations.Resend: finding invitation: %w", err)
	}

	// 2. Check status is "pending" (reject accepted/declined/revoked with 409 per Req 12.9).
	now := s.clock.Now()
	effective := EffectiveStatus(inv.Status, inv.ExpiresAt, now)
	if effective != StatusPending {
		return "", &InvitationError{
			Status:  409,
			Code:    "invitation_not_pending",
			Message: fmt.Sprintf("cannot resend an invitation with status %q", string(effective)),
			Details: map[string]any{"current_status": string(effective)},
		}
	}

	// 3. Rate limit: 3 resends per invitation per hour (Req 12.4).
	decision := s.rate.Allow(ctx, ratelimit.LimitInviteResend, invitationID)
	if !decision.Allowed {
		return "", &InvitationError{
			Status:  429,
			Code:    "resend_rate_limited",
			Message: "resend rate limit exceeded for this invitation",
			Details: map[string]any{
				"retry_after_seconds": int(decision.RetryAfter.Seconds()),
			},
		}
	}

	// 4. Generate a new token via newToken().
	plaintext, tokenHash, tokenErr := newToken()
	if tokenErr != nil {
		return "", fmt.Errorf("invitations.Resend: generating token: %w", tokenErr)
	}

	// 5. Update: token_hash=new hash, expires_at=now+168h, last_sent_at=now, $inc resend_count.
	expiresAt := now.Add(168 * time.Hour)
	update := bson.M{
		"$set": bson.M{
			"token_hash":   tokenHash,
			"expires_at":   expiresAt,
			"last_sent_at": now,
			"updated_at":   now,
		},
		"$inc": bson.M{
			"resend_count": 1,
		},
	}

	filter := bson.M{
		"_id":          invitationID,
		"workspace_id": workspaceID,
		"status":       string(StatusPending),
	}

	result := coll.FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After))
	if result.Err() != nil {
		if result.Err() == mongo.ErrNoDocuments {
			// Race: status changed between our check and the update.
			return "", &InvitationError{
				Status:  409,
				Code:    "invitation_not_pending",
				Message: "invitation is no longer pending",
			}
		}
		return "", fmt.Errorf("invitations.Resend: updating invitation: %w", result.Err())
	}

	// 6. Return the new plaintext token (for the email).
	return plaintext, nil
}

// Revoke sets status to "revoked", invalidates the token, and writes one audit
// entry naming the revoking user, workspace, and invited email (Req 12.2, 12.12).
// Rejects an invitation already accepted, declined, or revoked with 409 (Req 12.9).
//
// Requirements: 12.2, 12.9, 12.12
func (s *Service) Revoke(ctx context.Context, workspaceID, invitationID, actorUserID string) error {
	coll := configs.MI.DB.Collection("invitations")

	// 1. Find the invitation by ID and workspace_id.
	var inv Invitation
	err := coll.FindOne(ctx, bson.M{
		"_id":          invitationID,
		"workspace_id": workspaceID,
	}).Decode(&inv)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &InvitationError{
				Status:  404,
				Code:    "invitation_not_found",
				Message: "invitation not found",
			}
		}
		return fmt.Errorf("invitations.Revoke: finding invitation: %w", err)
	}

	// 2. Check status is "pending" (reject with 409 if not).
	now := s.clock.Now()
	effective := EffectiveStatus(inv.Status, inv.ExpiresAt, now)
	if effective != StatusPending {
		return &InvitationError{
			Status:  409,
			Code:    "invitation_not_pending",
			Message: fmt.Sprintf("cannot revoke an invitation with status %q", string(effective)),
			Details: map[string]any{"current_status": string(effective)},
		}
	}

	// 3. Update: status="revoked", revoked_at=now, revoked_by=actorUserID, token_hash="" (invalidate).
	filter := bson.M{
		"_id":          invitationID,
		"workspace_id": workspaceID,
		"status":       string(StatusPending),
	}
	update := bson.M{
		"$set": bson.M{
			"status":     string(StatusRevoked),
			"revoked_at": now,
			"revoked_by": actorUserID,
			"token_hash": "",
			"updated_at": now,
		},
	}

	result := coll.FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After))
	if result.Err() != nil {
		if result.Err() == mongo.ErrNoDocuments {
			// Race: status changed between our check and the update.
			return &InvitationError{
				Status:  409,
				Code:    "invitation_not_pending",
				Message: "invitation is no longer pending",
			}
		}
		return fmt.Errorf("invitations.Revoke: updating invitation: %w", result.Err())
	}

	// 4. Write audit entry (Req 12.12).
	s.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionInvitationRevoked,
		TargetKind:  "invitation",
		TargetID:    invitationID,
		Previous: map[string]any{
			"status": string(StatusPending),
		},
		New: map[string]any{
			"status": string(StatusRevoked),
			"email":  inv.Email,
		},
	})

	return nil
}

// SweepExpired sets every stored pending row past its expiry to "expired".
// Run at least every 24 hours from the scheduler (Req 12.8).
// Retains every invitation record — no delete path and no TTL index (Req 12.10).
//
// Requirements: 12.8, 12.10
func (s *Service) SweepExpired(ctx context.Context) (int64, error) {
	coll := configs.MI.DB.Collection("invitations")
	now := s.clock.Now()

	// UpdateMany: filter={status: "pending", expires_at: {$lt: now}}
	// update={$set: {status: "expired", updated_at: now}}
	filter := bson.M{
		"status":     string(StatusPending),
		"expires_at": bson.M{"$lt": now},
	}
	update := bson.M{
		"$set": bson.M{
			"status":     string(StatusExpired),
			"updated_at": now,
		},
	}

	result, err := coll.UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, fmt.Errorf("invitations.SweepExpired: %w", err)
	}

	return result.ModifiedCount, nil
}

// Resolve looks up an invitation by its plaintext token and returns public info
// BEFORE requiring authentication. (Req 11.1)
//
// Returns:
// - 404 invitation_not_found if no match (Req 11.2)
// - 410 invitation_revoked if status is revoked (Req 11.12)
// - 410 invitation_expired if expired (Req 11.13)
// - 410 if workspace deletion is scheduled or complete (Req 11.14)
// - 200 with PublicView otherwise
func (s *Service) Resolve(ctx context.Context, token, ip string) (PublicView, error) {
	// 1. Hash the token.
	hash := hashToken(token)

	// 2. Find invitation by token_hash.
	coll := configs.MI.DB.Collection("invitations")
	var inv Invitation
	err := coll.FindOne(ctx, bson.M{"token_hash": hash}).Decode(&inv)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return PublicView{}, &InvitationError{
				Status:  404,
				Code:    "invitation_not_found",
				Message: "no invitation matches the provided token",
			}
		}
		return PublicView{}, fmt.Errorf("invitations.Resolve: finding by token_hash: %w", err)
	}

	// 4. Compute effective status (pure function).
	effective := EffectiveStatus(inv.Status, inv.ExpiresAt, s.clock.Now())

	// 5. If revoked → 410 (Req 11.12).
	if effective == StatusRevoked {
		return PublicView{}, &InvitationError{
			Status:  410,
			Code:    "invitation_revoked",
			Message: "this invitation has been revoked",
		}
	}

	// 6. If expired → 410 with can_request_new (Req 11.13).
	if effective == StatusExpired {
		return PublicView{}, &InvitationError{
			Status:  410,
			Code:    "invitation_expired",
			Message: "this invitation has expired",
			Details: map[string]any{"can_request_new": true},
		}
	}

	// If already accepted or declined, treat as not found to avoid information leakage.
	if effective == StatusAccepted || effective == StatusDeclined {
		return PublicView{}, &InvitationError{
			Status:  404,
			Code:    "invitation_not_found",
			Message: "no invitation matches the provided token",
		}
	}

	// 7. Check workspace deletion state → 410 if scheduled or deleted (Req 11.14).
	workspacesColl := configs.MI.DB.Collection("workspaces")
	var ws workspaces.Workspace
	wsErr := workspacesColl.FindOne(ctx, bson.M{"_id": inv.WorkspaceID}).Decode(&ws)
	if wsErr != nil {
		if wsErr == mongo.ErrNoDocuments {
			return PublicView{}, &InvitationError{
				Status:  410,
				Code:    "workspace_deleted",
				Message: "the workspace associated with this invitation no longer exists",
			}
		}
		return PublicView{}, fmt.Errorf("invitations.Resolve: finding workspace: %w", wsErr)
	}
	if ws.DeletionState == workspaces.DeletionScheduled || ws.DeletionState == workspaces.DeletionDeleted {
		return PublicView{}, &InvitationError{
			Status:  410,
			Code:    "workspace_deleted",
			Message: "the workspace associated with this invitation is no longer available",
		}
	}

	// 8. Mask the invited email.
	maskedEmail := identity.MaskEmail(inv.Email)

	// 9. Look up the inviter display name.
	inviterName := "A team member" // fallback
	if inv.InvitedBy != "" {
		inviter, inviterErr := users.GetUserByID(ctx, inv.InvitedBy)
		if inviterErr == nil {
			name := buildDisplayName(inviter.FirstName, inviter.LastName)
			if name != "" {
				inviterName = name
			}
		}
	}

	// 10. Return PublicView.
	return PublicView{
		WorkspaceName: ws.Name,
		Role:          inv.Role,
		InviterName:   inviterName,
		InvitedEmail:  maskedEmail,
	}, nil
}

// Decline declines an invitation using the token as the sole authorization.
// No authentication required. (Req 12.5, 12.6)
// Sets status to "declined", creates zero memberships, notifies the inviter.
func (s *Service) Decline(ctx context.Context, token string) error {
	// 1. Hash the token, find the invitation.
	hash := hashToken(token)
	coll := configs.MI.DB.Collection("invitations")

	var inv Invitation
	err := coll.FindOne(ctx, bson.M{"token_hash": hash}).Decode(&inv)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &InvitationError{
				Status:  404,
				Code:    "invitation_not_found",
				Message: "no invitation matches the provided token",
			}
		}
		return fmt.Errorf("invitations.Decline: finding by token_hash: %w", err)
	}

	// 2. Compute effective status.
	effective := EffectiveStatus(inv.Status, inv.ExpiresAt, s.clock.Now())

	// 3. If not pending → 409 (can't decline accepted/revoked/expired/declined).
	if effective != StatusPending {
		return &InvitationError{
			Status:  409,
			Code:    "invitation_not_pending",
			Message: fmt.Sprintf("cannot decline an invitation with status %q", string(effective)),
			Details: map[string]any{"current_status": string(effective)},
		}
	}

	// 4. Set status to "declined", declined_at to now.
	// 5. Invalidate the token (clear token_hash).
	now := s.clock.Now()
	update := bson.M{
		"$set": bson.M{
			"status":      string(StatusDeclined),
			"declined_at": now,
			"token_hash":  "", // invalidate the token
			"updated_at":  now,
		},
	}

	_, updateErr := coll.UpdateOne(ctx, bson.M{"_id": inv.ID, "status": string(StatusPending)}, update)
	if updateErr != nil {
		return fmt.Errorf("invitations.Decline: updating invitation: %w", updateErr)
	}

	// 6. No audit entry needed for decline (only revoke gets audited per Req 12.12).
	// But we do notify the inviter (Req 12.5).
	// Notification is best-effort — failure does not roll back the decline.
	// (Notifications are handled by the route layer or a separate notifier;
	// the service marks the decline and the caller is responsible for triggering notification.)

	return nil
}

// Accept processes an invitation acceptance.
//
// Steps:
//  1. Hash the token, find the invitation
//  2. Compare normalized authenticated email with invited email (Req 11.4)
//     - Mismatch → 403 invitation_email_mismatch with masked invited email
//  3. Check effective status is pending (reject expired/revoked/accepted)
//  4. Create the membership via MembershipService.Create
//     - Duplicate key → 200 naming existing membership (Req 11.11)
//  5. Set status to "accepted", revoke other pending rows for (workspace, email) (Req 11.17)
//  6. Write audit entry (Req 23.1 — ActionInvitationAccepted)
//
// Requirements: 11.3, 11.4, 11.6, 11.7, 11.8, 11.9, 11.10, 11.11, 11.15, 11.16, 11.17
func (s *Service) Accept(ctx context.Context, in AcceptInput) (AcceptResult, error) {
	// 1. Hash the token and find the invitation by token_hash.
	hash := hashToken(in.Token)
	coll := configs.MI.DB.Collection("invitations")

	var inv Invitation
	err := coll.FindOne(ctx, bson.M{"token_hash": hash}).Decode(&inv)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return AcceptResult{}, &InvitationError{
				Status:  404,
				Code:    "invitation_not_found",
				Message: "no invitation matches the provided token",
			}
		}
		return AcceptResult{}, fmt.Errorf("invitations.Accept: finding by token_hash: %w", err)
	}

	// 2. Compute effective status — reject if not pending (Req 11.12, 11.13).
	now := s.clock.Now()
	effective := EffectiveStatus(inv.Status, inv.ExpiresAt, now)
	if effective != StatusPending {
		code := "invitation_not_pending"
		status := 409
		switch effective {
		case StatusRevoked:
			code = "invitation_revoked"
			status = 410
		case StatusExpired:
			code = "invitation_expired"
			status = 410
		case StatusAccepted:
			// If already accepted, return the existing membership (Req 11.11).
			// Try by invitation_id first; fall back to (user_id, workspace_id)
			// because MembershipService.Create only sets InvitationID when the
			// membership was created via this invitation — a direct signup or a
			// membership where the invitation link wasn't propagated would
			// otherwise report "already accepted" for a user who IS a member.
			membershipsColl := configs.MI.DB.Collection("memberships")
			var existingMembership workspaces.Membership
			findErr := membershipsColl.FindOne(ctx, bson.M{
				"invitation_id": inv.ID,
				"workspace_id":  inv.WorkspaceID,
			}).Decode(&existingMembership)
			if findErr != nil {
				findErr = membershipsColl.FindOne(ctx, bson.M{
					"user_id":      in.UserID,
					"workspace_id": inv.WorkspaceID,
					"status":       workspaces.MembershipActive,
				}).Decode(&existingMembership)
			}
			if findErr == nil {
				return AcceptResult{
					MembershipID:  existingMembership.ID,
					WorkspaceID:   existingMembership.WorkspaceID,
					Role:          string(existingMembership.Role),
					AlreadyMember: true,
				}, nil
			}
			// Fall through to the error if we can't find the membership.
			code = "invitation_already_accepted"
		case StatusDeclined:
			code = "invitation_declined"
		}
		return AcceptResult{}, &InvitationError{
			Status:  status,
			Code:    code,
			Message: fmt.Sprintf("cannot accept an invitation with status %q", string(effective)),
			Details: map[string]any{"current_status": string(effective)},
		}
	}

	// 3. Compare normalized authenticated email with invited email (Req 11.4).
	normalizedAuth := identity.NormalizeEmail(in.AuthenticatedEmail)
	if normalizedAuth != inv.Email {
		return AcceptResult{}, &InvitationError{
			Status:  403,
			Code:    "invitation_email_mismatch",
			Message: "the authenticated email does not match the invited email",
			Details: map[string]any{
				"invited_email": identity.MaskEmail(inv.Email),
			},
		}
	}

	// 4. Create membership via MembershipService.Create.
	// Duplicate key → return AcceptResult{AlreadyMember: true} with existing membership (Req 11.11).
	membership, createErr := s.membership.Create(ctx, workspaces.CreateMembershipInput{
		WorkspaceID:    inv.WorkspaceID,
		UserID:         in.UserID,
		Role:           workspaces.Role(inv.Role),
		StytchMemberID: in.StytchMemberID,
		InvitedBy:      inv.InvitedBy,
		InvitationID:   inv.ID,
	})
	if createErr != nil {
		return AcceptResult{}, fmt.Errorf("invitations.Accept: creating membership: %w", createErr)
	}

	// Check if this was a duplicate (MembershipService.Create returns the existing one on dup key).
	// InvitationID != inv.ID catches direct-signup memberships and memberships
	// created via a different invitation. CreatedAt.Before(now) additionally
	// catches the race where a concurrent Accept for this SAME invitation won
	// first: Create then returns an existing membership whose InvitationID
	// already equals inv.ID, which the ID comparison alone would miss.
	alreadyMember := membership.InvitationID != inv.ID || membership.CreatedAt.Before(now)

	// 5. Set invitation status to "accepted", accepted_at=now, accepted_user_id=in.UserID.
	acceptUpdate := bson.M{
		"$set": bson.M{
			"status":           string(StatusAccepted),
			"accepted_at":      now,
			"accepted_user_id": in.UserID,
			"token_hash":       "", // invalidate the token
			"updated_at":       now,
		},
	}
	updateResult, updateErr := coll.UpdateOne(ctx, bson.M{
		"_id":    inv.ID,
		"status": string(StatusPending),
	}, acceptUpdate)
	if updateErr != nil {
		return AcceptResult{}, fmt.Errorf("invitations.Accept: setting accepted status: %w", updateErr)
	}
	// A concurrent Accept/decline/revoke may have already flipped the status
	// between step 2's read and this write, matching zero documents. The
	// membership above already exists, but the invitation record itself
	// would be stuck in "pending" forever if this went unchecked.
	if updateResult.ModifiedCount == 0 {
		var currentInv Invitation
		if findErr := coll.FindOne(ctx, bson.M{"_id": inv.ID}).Decode(&currentInv); findErr == nil {
			return AcceptResult{}, &InvitationError{
				Status:  409,
				Code:    "invitation_not_pending",
				Message: fmt.Sprintf("invitation status changed to %q", currentInv.Status),
				Details: map[string]any{"current_status": currentInv.Status},
			}
		}
		return AcceptResult{}, fmt.Errorf("invitations.Accept: invitation status changed concurrently")
	}

	// 6. Revoke other pending invitations for same (workspace_id, email) (Req 11.17).
	_, revokeErr := coll.UpdateMany(ctx, bson.M{
		"workspace_id": inv.WorkspaceID,
		"email":        inv.Email,
		"status":       string(StatusPending),
		"_id":          bson.M{"$ne": inv.ID},
	}, bson.M{
		"$set": bson.M{
			"status":     string(StatusRevoked),
			"token_hash": "",
			"updated_at": now,
		},
	})
	if revokeErr != nil {
		// Log but do not fail the acceptance (best effort for revocation of others).
		slog.ErrorContext(ctx, "invitations.Accept: revoking other pending invitations",
			"workspace_id", inv.WorkspaceID,
			"email", inv.Email,
			"accepted_invitation_id", inv.ID,
			"error", revokeErr.Error(),
		)
	}

	// 7. Write audit entry (Req 23.1 — ActionInvitationAccepted).
	s.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: inv.WorkspaceID,
		ActorUserID: in.UserID,
		ActorKind:   "member",
		Action:      audit.ActionInvitationAccepted,
		TargetKind:  "invitation",
		TargetID:    inv.ID,
		New: map[string]any{
			"email":         inv.Email,
			"role":          inv.Role,
			"membership_id": membership.ID,
		},
	})

	return AcceptResult{
		MembershipID:  membership.ID,
		WorkspaceID:   inv.WorkspaceID,
		Role:          string(membership.Role),
		AlreadyMember: alreadyMember,
	}, nil
}

// ListResult is the response from List.
type ListResult struct {
	Invitations []InvitationView `json:"invitations"`
}

// InvitationView is the admin view of an invitation (for workspace members).
// It includes status, role, email, and expiry but ZERO token values (Req 10.17).
type InvitationView struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	Status        string    `json:"status"` // effective status
	InvitedBy     string    `json:"invited_by"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	DeliveryState string    `json:"delivery_state"`
	ResendCount   int       `json:"resend_count"`
}

// List returns every invitation for the workspace with its status, role, email,
// and expiry. Returns ZERO token values (Req 10.17).
//
// Requirements: 10.17
func (s *Service) List(ctx context.Context, workspaceID string) (ListResult, error) {
	coll := configs.MI.DB.Collection("invitations")

	// Query invitations where workspace_id = workspaceID, sorted by created_at descending.
	filter := bson.M{"workspace_id": workspaceID}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})

	cursor, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return ListResult{}, fmt.Errorf("invitations.List: querying invitations: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	now := s.clock.Now()
	var views []InvitationView

	for cursor.Next(ctx) {
		var inv Invitation
		if decErr := cursor.Decode(&inv); decErr != nil {
			return ListResult{}, fmt.Errorf("invitations.List: decoding invitation: %w", decErr)
		}

		// Compute effective status (pending past expiry reads as expired).
		effective := EffectiveStatus(inv.Status, inv.ExpiresAt, now)

		// Map to InvitationView — explicitly exclude any token fields.
		views = append(views, InvitationView{
			ID:            inv.ID,
			Email:         inv.Email,
			Role:          inv.Role,
			Status:        string(effective),
			InvitedBy:     inv.InvitedBy,
			IssuedAt:      inv.IssuedAt,
			ExpiresAt:     inv.ExpiresAt,
			DeliveryState: inv.DeliveryState,
			ResendCount:   inv.ResendCount,
		})
	}

	if err := cursor.Err(); err != nil {
		return ListResult{}, fmt.Errorf("invitations.List: cursor error: %w", err)
	}

	// Return an empty slice rather than nil for consistent JSON serialization.
	if views == nil {
		views = []InvitationView{}
	}

	return ListResult{Invitations: views}, nil
}

// rolePermissionSummary maps each invitable role to a plain-language summary
// of the permissions that role grants. Used by BuildInvitationEmail (Req 10.11).
var rolePermissionSummary = map[string]string{
	"admin":   "manage team members, post and manage job vacancies, view applications, view invoices and payouts, and view the audit log",
	"hr":      "post and manage job vacancies, review applications, and manage company profiles",
	"finance": "manage billing, payment methods, invoices, payouts, and subscriptions",
}

// roleDisplayName maps each invitable role to a nicely formatted display string.
var roleDisplayName = map[string]string{
	"admin":   "Admin",
	"hr":      "HR",
	"finance": "Finance",
}

// BuildInvitationEmail constructs the invitation email content.
// The email names the workspace, the inviter's display name, the offered role,
// a plain-language summary of the permissions that role grants, and the expiry date.
//
// Requirements: 10.11
func BuildInvitationEmail(workspaceName, inviterName, role string, expiresAt time.Time) (subject string, body string) {
	subject = fmt.Sprintf("You've been invited to join %s", workspaceName)

	displayRole := roleDisplayName[role]
	if displayRole == "" {
		displayRole = role
	}

	permSummary := rolePermissionSummary[role]
	if permSummary == "" {
		permSummary = "access workspace resources"
	}

	expiryStr := expiresAt.Format("January 2, 2006")

	body = fmt.Sprintf(
		"%s has invited you to join %s as %s.\n\n"+
			"As %s, you'll be able to %s.\n\n"+
			"This invitation expires on %s.",
		inviterName, workspaceName, displayRole,
		displayRole, permSummary,
		expiryStr,
	)

	return subject, body
}

// buildDisplayName constructs a display name from first and last names.
func buildDisplayName(first, last string) string {
	if first == "" && last == "" {
		return ""
	}
	if first == "" {
		return last
	}
	if last == "" {
		return first
	}
	return first + " " + last
}
