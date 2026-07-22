package notifications

import (
	"context"
	"testing"
)

type notificationCreatorStub struct {
	created []Notification
}

func (s *notificationCreatorStub) Create(_ context.Context, notification Notification) error {
	s.created = append(s.created, notification)
	return nil
}

func (s *notificationCreatorStub) CreateMany(_ context.Context, notifications []Notification) error {
	s.created = append(s.created, notifications...)
	return nil
}

type emailSenderStub struct {
	messages []emailMessage
}

type emailMessage struct {
	to      string
	subject string
	body    string
}

func (s *emailSenderStub) Send(_ context.Context, to, subject, body string) error {
	s.messages = append(s.messages, emailMessage{to: to, subject: subject, body: body})
	return nil
}

type smsSenderStub struct {
	messages []smsMessage
}

type smsMessage struct {
	to   string
	body string
}

func (s *smsSenderStub) Send(to, message string) error {
	s.messages = append(s.messages, smsMessage{to: to, body: message})
	return nil
}

func TestServiceNotifyNewApplication(t *testing.T) {
	store := &notificationCreatorStub{}
	email := &emailSenderStub{}
	service := NewService(store, email, &smsSenderStub{})

	err := service.NotifyNewApplication(context.Background(), NewApplicationEvent{
		EmployerID:    "employer-1",
		EmployerEmail: "employer@example.com",
		ApplicantName: "Amina Otieno",
		VacancyID:     "vacancy-1",
		VacancyTitle:  "Electrician",
	})
	if err != nil {
		t.Fatalf("NotifyNewApplication returned error: %v", err)
	}

	if len(store.created) != 1 {
		t.Fatalf("created notifications = %d, want 1", len(store.created))
	}
	got := store.created[0]
	if got.UserID != "employer-1" || got.Type != TypeNewApplication {
		t.Fatalf("unexpected notification: %#v", got)
	}
	if got.ActionURL != "/vacancies/vacancy-1/applications" {
		t.Fatalf("action URL = %q", got.ActionURL)
	}
	if len(email.messages) != 1 || email.messages[0].to != "employer@example.com" {
		t.Fatalf("unexpected email messages: %#v", email.messages)
	}
}

func TestServiceNotifyApplicationDecision(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		wantSMSCount int
	}{
		{name: "accepted sends sms", status: "accepted", wantSMSCount: 1},
		{name: "rejected does not send sms", status: "rejected", wantSMSCount: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &notificationCreatorStub{}
			sms := &smsSenderStub{}
			service := NewService(store, &emailSenderStub{}, sms)

			err := service.NotifyApplicationDecision(context.Background(), ApplicationDecisionEvent{
				WorkerID:     "worker-1",
				WorkerPhone:  "254700000000",
				VacancyID:    "vacancy-1",
				VacancyTitle: "Electrician",
				Status:       test.status,
			})
			if err != nil {
				t.Fatalf("NotifyApplicationDecision returned error: %v", err)
			}
			if len(store.created) != 1 {
				t.Fatalf("created notifications = %d, want 1", len(store.created))
			}
			if store.created[0].Type != TypeApplicationStatus {
				t.Fatalf("notification type = %q", store.created[0].Type)
			}
			if len(sms.messages) != test.wantSMSCount {
				t.Fatalf("sms messages = %d, want %d", len(sms.messages), test.wantSMSCount)
			}
		})
	}
}

func TestServiceNotifyMatchingWorkers(t *testing.T) {
	store := &notificationCreatorStub{}
	service := NewService(store, &emailSenderStub{}, &smsSenderStub{})

	err := service.NotifyMatchingWorkers(context.Background(), NewVacancyEvent{
		WorkerIDs:    []string{"worker-1", "worker-2"},
		VacancyID:    "vacancy-1",
		VacancyTitle: "Electrician",
		Skill:        "Electrician",
	})
	if err != nil {
		t.Fatalf("NotifyMatchingWorkers returned error: %v", err)
	}
	if len(store.created) != 2 {
		t.Fatalf("created notifications = %d, want 2", len(store.created))
	}
	for _, notification := range store.created {
		if notification.Type != TypeSkillMatch || notification.ActionURL != "/vacancies" {
			t.Fatalf("unexpected notification: %#v", notification)
		}
	}
}
