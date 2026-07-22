package notifications

import (
	"context"
	"fmt"
	"html"
	"strings"

	"backend/configs"
)

type notificationCreator interface {
	Create(context.Context, Notification) error
	CreateMany(context.Context, []Notification) error
}

type Service struct {
	store notificationCreator
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

func NewService(store notificationCreator) *Service {
	return &Service{store: store}
}

func NewDefaultService() *Service {
	return NewService(NewMongoStore())
}

func (s *Service) NotifyNewApplication(ctx context.Context, event NewApplicationEvent) error {
	actionURL := "/vacancies/" + event.VacancyID + "/applications"
	reviewURL := strings.TrimRight(configs.AppPublicURL(), "/") + actionURL
	notification := Notification{
		UserID:    event.EmployerID,
		Type:      TypeNewApplication,
		Title:     "New application",
		Message:   event.ApplicantName + " applied for " + event.VacancyTitle + ".",
		ActionURL: actionURL,
	}
	if strings.TrimSpace(event.EmployerEmail) != "" {
		notification.Delivery = &Delivery{
			Channel: DeliveryChannelEmail,
			To:      event.EmployerEmail,
			Subject: "New application for " + event.VacancyTitle,
			Body: fmt.Sprintf(
				"<p>%s applied for <strong>%s</strong>.</p><p><a href=\"%s\">Review the application</a></p>",
				html.EscapeString(event.ApplicantName),
				html.EscapeString(event.VacancyTitle),
				html.EscapeString(reviewURL),
			),
		}
	}
	return s.store.Create(ctx, notification)
}

func (s *Service) NotifyApplicationDecision(ctx context.Context, event ApplicationDecisionEvent) error {
	notification := Notification{
		UserID:    event.WorkerID,
		Type:      TypeApplicationStatus,
		Title:     "Application " + event.Status,
		Message:   "Your application for " + event.VacancyTitle + " was " + event.Status + ".",
		ActionURL: "/vacancies",
	}
	if event.Status == "accepted" && strings.TrimSpace(event.WorkerPhone) != "" {
		notification.Delivery = &Delivery{
			Channel: DeliveryChannelSMS,
			To:      event.WorkerPhone,
			Body: "Congratulations! You have been hired for " + event.VacancyTitle +
				". Sign in to Fundis for the vacancy details.",
		}
	}
	return s.store.Create(ctx, notification)
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
