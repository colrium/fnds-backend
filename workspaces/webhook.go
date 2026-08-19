package workspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/identity"
	"backend/stytch"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// WebhookConsumer handles Stytch webhook deliveries.
type WebhookConsumer struct {
	adapter     *stytch.Adapter
	clock       stytch.Clock
	memberships *MembershipService
	workspaces  *WorkspaceService
	audit       *audit.Logger
}

// NewWebhookConsumer constructs a WebhookConsumer.
func NewWebhookConsumer(adapter *stytch.Adapter, clock stytch.Clock, memberships *MembershipService, workspaces *WorkspaceService, auditLogger *audit.Logger) *WebhookConsumer {
	return &WebhookConsumer{
		adapter:     adapter,
		clock:       clock,
		memberships: memberships,
		workspaces:  workspaces,
		audit:       auditLogger,
	}
}

// Handle processes a Stytch webhook delivery.
//
// Steps:
// 1. Read raw body through io.LimitReader (before any body-consuming middleware)
// 2. Verify signature — invalid -> 401, one warning, zero state change (Req 19.2)
// 3. Timestamp skew > 5 min -> 400, zero state change (Req 19.3)
// 4. Insert processed_webhook_events {_id: svix-id} BEFORE processing (Req 19.4)
//   - Duplicate key -> 200, zero additional state change (Req 19.5)
//
// 5. Process the event (delegates to event handlers - task 16.2)
// 6. On processing failure: delete the event record, return 500 so Stytch retries (Req 19.12)
//
// Requirements: 19.1, 19.2, 19.3, 19.4, 19.5, 19.12
func (wc *WebhookConsumer) Handle(c echo.Context) error {
	// Step 1: Read the raw body.
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20)) // 1MB limit
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"success": false,
			"message": "failed to read request body",
		})
	}

	// Step 2: Verify the webhook signature (Req 19.1).
	now := wc.clock.Now()
	event, verifyErr := stytch.VerifyWebhook(
		wc.adapter.Config().WebhookSecret,
		c.Request().Header,
		body,
		now,
	)
	if verifyErr != nil {
		// Invalid signature or timestamp -> 401 with one warning (Req 19.2, 19.3)
		slog.Warn("webhook: signature verification failed",
			"error", verifyErr.Error(),
			"source_ip", c.RealIP(),
		)
		return c.JSON(http.StatusUnauthorized, map[string]any{
			"success": false,
			"message": "webhook signature verification failed",
		})
	}

	// Step 4: Insert processed_webhook_events {_id: svix-id} BEFORE processing.
	ctx := c.Request().Context()
	eventRecord := ProcessedWebhookEvent{
		ID:             event.ID,
		EventTimestamp: event.Timestamp,
		ProcessedAt:    now,
		ExpiresAt:      now.Add(30 * 24 * time.Hour), // 30 days
	}

	eventsColl := configs.MI.DB.Collection("processed_webhook_events")
	_, insertErr := eventsColl.InsertOne(ctx, eventRecord)
	if insertErr != nil {
		if mongo.IsDuplicateKeyError(insertErr) {
			// Already processed -> 200, zero additional state change (Req 19.5).
			return c.JSON(http.StatusOK, map[string]any{
				"success": true,
				"message": "event already processed",
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "internal error recording event",
		})
	}

	// Step 5: Process the event (placeholder — event classification is task 16.2).
	processErr := wc.processEvent(ctx, event, body)
	if processErr != nil {
		// Step 6: On failure, delete the event record so Stytch retries (Req 19.12).
		if _, delErr := eventsColl.DeleteOne(ctx, bson.M{"_id": event.ID}); delErr != nil {
			// The record stays, so Stytch's retry will hit the duplicate-key
			// path and be acknowledged without reprocessing — a stuck event
			// rather than a duplicated one, but it needs to be visible.
			slog.ErrorContext(ctx, "webhook: processing failed and event record could not be deleted, retry will be deduplicated away",
				"event_id", event.ID,
				"process_error", processErr.Error(),
				"delete_error", delErr.Error(),
			)
		} else {
			slog.ErrorContext(ctx, "webhook: processing failed, event record deleted for retry",
				"event_id", event.ID,
				"error", processErr.Error(),
			)
		}
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "processing failed",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"success": true,
		"message": "event processed",
	})
}

// processEvent classifies the event by type and applies the corresponding
// state change through MembershipService/WorkspaceService, never through a
// raw collection write in this file, so "the membership document is
// authoritative" (Req 19.13) is enforced by the same code path as the HTTP
// surface. An unrecognized event type is acknowledged with one info log
// rather than treated as a failure (Req 19.11).
func (wc *WebhookConsumer) processEvent(ctx context.Context, event stytch.Event, body []byte) error {
	switch event.Type {
	case stytch.EventMemberEmailUpdated:
		return wc.handleMemberEmailUpdated(ctx, body)
	case stytch.EventMemberDeleted:
		return wc.handleMemberDeleted(ctx, event, body)
	case stytch.EventOrganizationDeleted:
		return wc.handleOrganizationDeleted(ctx, event, body)
	default:
		slog.InfoContext(ctx, "webhook: unhandled event type acknowledged",
			"event_id", event.ID,
			"event_type", event.Type,
		)
		return nil
	}
}

// handleMemberEmailUpdated applies Requirement 5 criteria 13/14 via
// identity.ApplyEmailChange (Req 19.6).
func (wc *WebhookConsumer) handleMemberEmailUpdated(ctx context.Context, body []byte) error {
	var payload stytch.WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}

	memberID := payload.MemberID
	if memberID == "" {
		memberID = payload.Member.MemberID
	}
	if memberID == "" || payload.Member.Email == "" {
		slog.WarnContext(ctx, "webhook: member.email.update missing member_id or email, ignoring")
		return nil
	}

	return identity.ApplyEmailChange(ctx, wc.audit, memberID, payload.Member.Email, wc.clock.Now())
}

// handleMemberDeleted applies Requirement 19 criteria 7/8: a non-owner
// Membership is set to removed; an owner Membership is retained and its
// Stytch_Member is recreated.
func (wc *WebhookConsumer) handleMemberDeleted(ctx context.Context, event stytch.Event, body []byte) error {
	var payload stytch.WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	memberID := payload.MemberID
	if memberID == "" {
		memberID = payload.Member.MemberID
	}
	if memberID == "" {
		slog.WarnContext(ctx, "webhook: member.delete missing member_id, ignoring")
		return nil
	}

	membershipsColl := configs.MI.DB.Collection("memberships")
	var membership Membership
	if err := membershipsColl.FindOne(ctx, bson.M{"stytch_member_id": memberID}).Decode(&membership); err != nil {
		if err == mongo.ErrNoDocuments {
			// No Fundis membership tracks this member — nothing to reconcile.
			return nil
		}
		return err
	}

	if membership.Role != RoleOwner {
		return wc.memberships.SetStatusFromWebhook(ctx, membership.ID, MembershipRemoved, event.Timestamp)
	}

	workspacesColl := configs.MI.DB.Collection("workspaces")
	var ws Workspace
	if err := workspacesColl.FindOne(ctx, bson.M{"_id": membership.WorkspaceID}).Decode(&ws); err != nil {
		return err
	}
	slog.ErrorContext(ctx, "webhook: stytch member behind an owner membership was deleted, recreating",
		"workspace_id", membership.WorkspaceID,
		"membership_id", membership.ID,
	)
	if err := wc.memberships.RecreateOwnerMember(ctx, membership, ws); err != nil {
		return fmt.Errorf("webhook: recreate owner member: %w", err)
	}
	return nil
}

// handleOrganizationDeleted applies Requirement 19 criterion 9.
func (wc *WebhookConsumer) handleOrganizationDeleted(ctx context.Context, event stytch.Event, body []byte) error {
	var payload stytch.WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if payload.OrganizationID == "" {
		slog.WarnContext(ctx, "webhook: organization.delete missing organization_id, ignoring")
		return nil
	}

	workspacesColl := configs.MI.DB.Collection("workspaces")
	var ws Workspace
	if err := workspacesColl.FindOne(ctx, bson.M{"stytch_organization_id": payload.OrganizationID}).Decode(&ws); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil
		}
		return err
	}

	return wc.workspaces.MarkDeletedFromWebhook(ctx, ws.ID, event.Timestamp)
}
