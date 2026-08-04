package notifications

import (
	"context"
	"fmt"
	"html"
	"strings"

	"backend/configs"
)

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

type QuotationReceivedEvent struct {
	CustomerID    string
	CustomerEmail string
	ArtisanName   string
	QuotationID   string
	JobRequestID  string
	JobTitle      string
	TotalAmount   float64
}

type QuotationDecisionEvent struct {
	ArtisanID    string
	ArtisanEmail string
	CustomerName string
	QuotationID  string
	JobTitle     string
	Status       string
}

func NewService(store notificationCreator) *Service {
	return &Service{store: store}
}

// NewDefaultService routes Create and CreateMany through the shared,
// invalidating default CachedStore so job-request and quotation producers
// keep the notifications feed cache correct. main.go must call
// ConfigureDefaultStore before any caller reaches this function; that
// ordering is a startup invariant, so a misconfigured call panics rather
// than propagating a nil store into request handling.
func NewDefaultService() *Service {
	store, err := DefaultStore()
	if err != nil {
		panic(err)
	}
	return NewService(store)
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

func (s *Service) NotifyQuotationReceived(ctx context.Context, event QuotationReceivedEvent) error {
	notification := Notification{
		UserID:    event.CustomerID,
		Type:      TypeQuotationReceived,
		Title:     "New quotation",
		Message:   event.ArtisanName + " sent you a quotation for " + event.JobTitle + ".",
		ActionURL: "/jobs/" + event.JobRequestID,
	}
	if strings.TrimSpace(event.CustomerEmail) != "" {
		notification.Delivery = &Delivery{
			Channel: DeliveryChannelEmail,
			To:      event.CustomerEmail,
			Subject: "New quotation from " + event.ArtisanName,
			Body: fmt.Sprintf(
				"<p>%s sent you a quotation for <strong>%s</strong> totalling KES %.2f.</p><p><a href=\"%s\">Review quotation</a></p>",
				html.EscapeString(event.ArtisanName),
				html.EscapeString(event.JobTitle),
				event.TotalAmount,
				html.EscapeString(strings.TrimRight(configs.AppPublicURL(), "/")+notification.ActionURL),
			),
		}
	}
	return s.store.Create(ctx, notification)
}

func (s *Service) NotifyQuotationDecision(ctx context.Context, event QuotationDecisionEvent) error {
	notification := Notification{
		UserID:    event.ArtisanID,
		Type:      TypeQuotationDecision,
		Title:     "Quotation " + event.Status,
		Message:   event.CustomerName + " " + event.Status + " your quotation for " + event.JobTitle + ".",
		ActionURL: "/quotations",
	}
	if strings.TrimSpace(event.ArtisanEmail) != "" {
		notification.Delivery = &Delivery{
			Channel: DeliveryChannelEmail,
			To:      event.ArtisanEmail,
			Subject: "Quotation " + event.Status,
			Body: fmt.Sprintf(
				"<p>%s %s your quotation for <strong>%s</strong>.</p><p><a href=\"%s\">Open quotations</a></p>",
				html.EscapeString(event.CustomerName),
				html.EscapeString(event.Status),
				html.EscapeString(event.JobTitle),
				html.EscapeString(strings.TrimRight(configs.AppPublicURL(), "/")+notification.ActionURL),
			),
		}
	}
	return s.store.Create(ctx, notification)
}
