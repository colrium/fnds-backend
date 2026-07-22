package notifications

import (
	"testing"
)

func TestNewEmailService(t *testing.T) {
	svc := NewEmailService("noreply@fundis.app")
	if svc == nil {
		t.Fatal("expected non-nil EmailService")
	}
	if svc.DefaultFrom != "noreply@fundis.app" {
		t.Errorf("expected default from 'noreply@fundis.app', got %q", svc.DefaultFrom)
	}
}

func TestSendWelcomeEmail(t *testing.T) {
	t.Setenv("SENDGRID_API_KEY", "")
	t.Setenv("SENDGRID_FROM_EMAIL", "")
	err := SendWelcomeEmail("test@example.com", "Test User")
	if err == nil {
		t.Error("expected configuration error")
	}
}

func TestSendOTPEmail(t *testing.T) {
	t.Setenv("SENDGRID_API_KEY", "")
	t.Setenv("SENDGRID_FROM_EMAIL", "")
	err := SendOTPEmail("test@example.com", "123456")
	if err == nil {
		t.Error("expected configuration error")
	}
}

func TestEmailServiceSend(t *testing.T) {
	t.Setenv("SENDGRID_API_KEY", "")
	svc := NewEmailService("noreply@fundis.app")
	err := svc.Send(EmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    "Hello",
	})
	if err == nil {
		t.Error("expected configuration error")
	}
}
