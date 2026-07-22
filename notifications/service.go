package notifications

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"backend/configs"
	"backend/integrations"
)

type notificationCreator interface {
	Create(context.Context, Notification) error
	CreateMany(context.Context, []Notification) error
}

type emailSender interface {
	Send(context.Context, string, string, string) error
}

type smsSender interface {
	Send(string, string) error
}

type Service struct {
	store notificationCreator
	email emailSender
	sms   smsSender
}

type NewApplicationEvent struct {
	EmployerID    string
	EmployerEmail string
	ApplicantName string
	VacancyID     string
	VacancyTitle  string
}

type ApplicationDecisionEvent struct {
	WorkerID     string
	WorkerPhone  string
	VacancyID    string
	VacancyTitle string
	Status       string
}

type NewVacancyEvent struct {
	WorkerIDs    []string
	VacancyID    string
	VacancyTitle string
	Skill        string
}

func NewService(store notificationCreator, email emailSender, sms smsSender) *Service {
	return &Service{store: store, email: email, sms: sms}
}

func NewDefaultService() *Service {
	return NewService(
		NewMongoStore(),
		integrations.NewSendGridClient(
			configs.SendGridAPIKey(),
			configs.SendGridFromEmail(),
			configs.SendGridFromName(),
		),
		&AdvantaSMS{},
	)
}

func (s *Service) NotifyNewApplication(ctx context.Context, event NewApplicationEvent) error {
	actionURL := "/vacancies/" + event.VacancyID + "/applications"
	notificationErr := s.store.Create(ctx, Notification{
		UserID:    event.EmployerID,
		Type:      TypeNewApplication,
		Title:     "New application",
		Message:   event.ApplicantName + " applied for " + event.VacancyTitle + ".",
		ActionURL: actionURL,
	})

	reviewURL := strings.TrimRight(configs.AppPublicURL(), "/") + actionURL
	emailErr := s.email.Send(
		ctx,
		event.EmployerEmail,
		"New application for "+event.VacancyTitle,
		fmt.Sprintf(
			"<p>%s applied for <strong>%s</strong>.</p><p><a href=\"%s\">Review the application</a></p>",
			html.EscapeString(event.ApplicantName),
			html.EscapeString(event.VacancyTitle),
			html.EscapeString(reviewURL),
		),
	)
	return errors.Join(notificationErr, emailErr)
}

func (s *Service) NotifyApplicationDecision(ctx context.Context, event ApplicationDecisionEvent) error {
	notificationErr := s.store.Create(ctx, Notification{
		UserID:    event.WorkerID,
		Type:      TypeApplicationStatus,
		Title:     "Application " + event.Status,
		Message:   "Your application for " + event.VacancyTitle + " was " + event.Status + ".",
		ActionURL: "/vacancies",
	})
	if event.Status != "accepted" {
		return notificationErr
	}

	smsErr := s.sms.Send(
		event.WorkerPhone,
		"Congratulations! You have been hired for "+event.VacancyTitle+
			". Sign in to Fundis for the vacancy details.",
	)
	return errors.Join(notificationErr, smsErr)
}

func (s *Service) NotifyMatchingWorkers(ctx context.Context, event NewVacancyEvent) error {
	notifications := make([]Notification, 0, len(event.WorkerIDs))
	for _, workerID := range event.WorkerIDs {
		notifications = append(notifications, Notification{
			UserID:    workerID,
			Type:      TypeSkillMatch,
			Title:     "New " + event.Skill + " vacancy",
			Message:   event.VacancyTitle + " matches your professional skill.",
			ActionURL: "/vacancies",
		})
	}
	return s.store.CreateMany(ctx, notifications)
}
