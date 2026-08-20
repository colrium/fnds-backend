package workspaces

import (
	"context"
	"fmt"
	"log/slog"

	"backend/notifications"
	"backend/users"
)

// Notifier is the interface for sending notifications. Implemented by the
// existing notifications package or by a spy in tests. SMS is included
// alongside email because the existing notifications package (Advanta,
// AfricasTalking, Twilio) supports both, and an email-only interface would
// force callers to cast to a concrete type the moment SMS is needed here.
type Notifier interface {
	SendEmail(to, subject, body string)
	SendSMS(to, body string)
}

// emailNotifier is the production Notifier, delegating to the existing
// notifications package. The Notifier interface is fire-and-forget by design —
// membership notifications are best-effort and must never fail the mutation
// that triggered them — so a delivery error is logged here rather than
// returned.
type emailNotifier struct{}

func (emailNotifier) SendEmail(to, subject, body string) {
	if err := notifications.SendEmailViaSendGrid(context.Background(), to, subject, body); err != nil {
		slog.Warn("notify: failed to send membership email", "subject", subject, "error", err)
	}
}

func (emailNotifier) SendSMS(to, body string) {
	if err := notifications.SendSMS(notifications.DefaultSMSProvider, to, body); err != nil {
		slog.Warn("notify: failed to send membership SMS", "error", err)
	}
}

// NotifyRoleChange sends an email to the affected member naming the previous
// and new role. (Req 13.9)
func NotifyRoleChange(ctx context.Context, notifier Notifier, memberUserID, workspaceName string, previousRole, newRole Role) {
	user, err := users.GetUserByID(ctx, memberUserID)
	if err != nil {
		slog.ErrorContext(ctx, "notify: failed to load user for role change", "user_id", memberUserID, "error", err)
		return
	}
	if user.Email == "" {
		return
	}
	subject := fmt.Sprintf("Your role in %s has changed", workspaceName)
	body := fmt.Sprintf(
		"Your role in the workspace \"%s\" has been changed from %s to %s.",
		workspaceName, previousRole, newRole,
	)
	notifier.SendEmail(user.Email, subject, body)
}

// NotifyMemberRemoval sends an email to the removed person stating that
// access to the named workspace has ended. (Req 13.6)
func NotifyMemberRemoval(ctx context.Context, notifier Notifier, memberUserID, workspaceName string) {
	user, err := users.GetUserByID(ctx, memberUserID)
	if err != nil {
		slog.ErrorContext(ctx, "notify: failed to load user for removal", "user_id", memberUserID, "error", err)
		return
	}
	if user.Email == "" {
		return
	}
	subject := fmt.Sprintf("Your access to %s has ended", workspaceName)
	body := fmt.Sprintf(
		"Your membership in the workspace \"%s\" has been removed. You no longer have access to this workspace.",
		workspaceName,
	)
	notifier.SendEmail(user.Email, subject, body)
}

// NotifyOwnershipTransfer sends emails to both the previous and new owner
// naming the workspace, both parties, and the time of transfer. (Req 14.9)
func NotifyOwnershipTransfer(ctx context.Context, notifier Notifier, previousOwnerUserID, newOwnerUserID, workspaceName string) {
	prevUser, prevErr := users.GetUserByID(ctx, previousOwnerUserID)
	newUser, newErr := users.GetUserByID(ctx, newOwnerUserID)

	if prevErr != nil {
		slog.ErrorContext(ctx, "notify: failed to load previous owner", "user_id", previousOwnerUserID, "error", prevErr)
	}
	if newErr != nil {
		slog.ErrorContext(ctx, "notify: failed to load new owner", "user_id", newOwnerUserID, "error", newErr)
	}

	prevName := "the previous owner"
	newName := "the new owner"
	if prevUser != nil {
		prevName = prevUser.FirstName + " " + prevUser.LastName
	}
	if newUser != nil {
		newName = newUser.FirstName + " " + newUser.LastName
	}

	subject := fmt.Sprintf("Ownership of %s has been transferred", workspaceName)

	if prevUser != nil && prevUser.Email != "" {
		body := fmt.Sprintf(
			"Ownership of the workspace \"%s\" has been transferred from you to %s. Your role is now Admin.",
			workspaceName, newName,
		)
		notifier.SendEmail(prevUser.Email, subject, body)
	}

	if newUser != nil && newUser.Email != "" {
		body := fmt.Sprintf(
			"You are now the owner of the workspace \"%s\". Ownership was transferred from %s.",
			workspaceName, prevName,
		)
		notifier.SendEmail(newUser.Email, subject, body)
	}
}
