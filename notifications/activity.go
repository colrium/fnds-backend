package notifications

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"backend/configs"
	"backend/integrations"
)

// SlackChannelType identifies the target Slack channel for routing.
type SlackChannelType string

const (
	// SlackChannelOps routes to the operations notifications channel.
	SlackChannelOps SlackChannelType = "ops_notifs"
)

// VacancyCreatedActivity carries vacancy-creation fields.
type VacancyCreatedActivity struct {
	VacancyID     string
	VacancyTitle  string
	CreatorUserID string
	CreatedAt     time.Time
}

// ApplicationActivity carries applicant PII + vacancy refs.
type ApplicationActivity struct {
	ApplicantName  string
	ApplicantEmail string
	ApplicantPhone string
	ApplicantID    string
	VacancyID      string
	VacancyTitle   string
	AppliedAt      time.Time
}

// RegistrationActivity carries new-user PII.
type RegistrationActivity struct {
	UserName     string
	UserEmail    string
	UserPhone    string
	UserID       string
	UserType     string
	RegisteredAt time.Time
}

// ProfileSubmittedActivity carries profile-submitted-for-review fields.
type ProfileSubmittedActivity struct {
	UserID      string
	UserName    string
	UserEmail   string
	SubmittedAt time.Time
}

// NotifyVacancyCreated builds and sends the vacancy activity message.
func NotifyVacancyCreated(ctx context.Context, a VacancyCreatedActivity) error {
	msg := integrations.BuildVacancyCreatedMessage(integrations.VacancyCreatedEvent{
		VacancyID:     a.VacancyID,
		VacancyTitle:  a.VacancyTitle,
		CreatorUserID: a.CreatorUserID,
		CreatedAt:     a.CreatedAt,
		AdminBaseURL:  configs.AdminDashboardURL(),
	})
	return sendActivity(ctx, msg, "vacancy_created", SlackChannelOps)
}

// NotifyApplicationCreated builds and sends the application activity message.
func NotifyApplicationCreated(ctx context.Context, a ApplicationActivity) error {
	msg := integrations.BuildApplicationMessage(integrations.ApplicationEvent{
		ApplicantName:  a.ApplicantName,
		ApplicantEmail: a.ApplicantEmail,
		ApplicantPhone: a.ApplicantPhone,
		ApplicantID:    a.ApplicantID,
		VacancyID:      a.VacancyID,
		VacancyTitle:   a.VacancyTitle,
		AppliedAt:      a.AppliedAt,
		AdminBaseURL:   configs.AdminDashboardURL(),
	})
	return sendActivity(ctx, msg, "application_created", SlackChannelOps)
}

// NotifyUserRegistered builds and sends the registration activity message.
func NotifyUserRegistered(ctx context.Context, a RegistrationActivity) error {
	msg := integrations.BuildRegistrationMessage(integrations.RegistrationEvent{
		UserName:     a.UserName,
		UserEmail:    a.UserEmail,
		UserPhone:    a.UserPhone,
		UserID:       a.UserID,
		UserType:     a.UserType,
		RegisteredAt: a.RegisteredAt,
		AdminBaseURL: configs.AdminDashboardURL(),
	})
	return sendActivity(ctx, msg, "user_registered", SlackChannelOps)
}

// NotifyProfileSubmitted builds and sends the profile-submitted-for-review activity message.
func NotifyProfileSubmitted(ctx context.Context, a ProfileSubmittedActivity) error {
	msg := integrations.BuildProfileSubmittedMessage(integrations.ProfileSubmittedEvent{
		UserID:       a.UserID,
		UserName:     a.UserName,
		UserEmail:    a.UserEmail,
		SubmittedAt:  a.SubmittedAt,
		AdminBaseURL: configs.AdminDashboardURL(),
	})
	return sendActivity(ctx, msg, "profile_submitted", SlackChannelOps)
}

// resolveWebhookURL selects the webhook URL based on the channel type.
func resolveWebhookURL(channelType SlackChannelType) string {
	switch channelType {
	case SlackChannelOps:
		if url := configs.SlackOpsNotifsURL(); url != "" {
			return url
		}
		return configs.SlackWebhookURL()
	default:
		return configs.SlackWebhookURL()
	}
}

// sendActivity centralizes delivery, skip/failure logging (PII-free).
func sendActivity(ctx context.Context, msg integrations.BlockKitMessage, kind string, channelType SlackChannelType) error {
	webhookURL := resolveWebhookURL(channelType)
	client := integrations.NewSlackActivityClient(webhookURL)
	err := client.Send(ctx, msg)
	switch {
	case errors.Is(err, integrations.ErrSlackWebhookNotConfigured):
		slog.Warn("slack activity webhook not configured; skipping", "activity", kind)
		return nil
	case err != nil:
		slog.Error("slack activity delivery failed", "activity", kind, "error", err)
		return err
	}
	return nil
}
