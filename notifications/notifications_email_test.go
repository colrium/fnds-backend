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
	err := SendWelcomeEmail("test@example.com", "Test User")
	if err != nil {
		t.Errorf("SendWelcomeEmail returned error: %v", err)
	}
}

func TestSendOTPEmail(t *testing.T) {
	err := SendOTPEmail("test@example.com", "123456")
	if err != nil {
		t.Errorf("SendOTPEmail returned error: %v", err)
	}
}

func TestEmailServiceSend(t *testing.T) {
	svc := NewEmailService("noreply@fundis.app")
	err := svc.Send(EmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    "Hello",
	})
	if err != nil {
		t.Errorf("EmailService.Send returned error: %v", err)
	}
}
