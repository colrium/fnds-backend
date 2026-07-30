package notifications

import (
	"context"
	"strings"
	"testing"
)

type recordingEmailSender struct {
	to      string
	subject string
	body    string
	err     error
}

func (s *recordingEmailSender) Send(_ context.Context, to, subject, body string) error {
	s.to = to
	s.subject = subject
	s.body = body
	return s.err
}

func TestNewEmailService(t *testing.T) {
	sender := &recordingEmailSender{}
	svc := newEmailService("noreply@fundis.app", sender)

	if svc == nil {
		t.Fatal("expected non-nil EmailService")
	}
	if svc.DefaultFrom != "noreply@fundis.app" {
		t.Errorf("expected default from 'noreply@fundis.app', got %q", svc.DefaultFrom)
	}
	if svc.sender != sender {
		t.Fatal("expected injected email sender")
	}
}

func TestSendWelcomeEmail(t *testing.T) {
	sender := &recordingEmailSender{}
	if err := sendWelcomeEmail(sender, "test@example.com", "Test User"); err != nil {
		t.Fatalf("sendWelcomeEmail returned error: %v", err)
	}

	if sender.to != "test@example.com" {
		t.Errorf("recipient = %q, want %q", sender.to, "test@example.com")
	}
	if sender.subject != "Welcome to Fundis" {
		t.Errorf("subject = %q, want %q", sender.subject, "Welcome to Fundis")
	}
	if !strings.Contains(sender.body, "Hello Test User") {
		t.Errorf("body does not contain recipient name: %q", sender.body)
	}
}

func TestSendOTPEmail(t *testing.T) {
	sender := &recordingEmailSender{}
	if err := sendOTPEmail(sender, "test@example.com", "123456"); err != nil {
		t.Fatalf("sendOTPEmail returned error: %v", err)
	}

	if sender.to != "test@example.com" {
		t.Errorf("recipient = %q, want %q", sender.to, "test@example.com")
	}
	if sender.subject != "Your Fundis Verification Code" {
		t.Errorf("subject = %q, want %q", sender.subject, "Your Fundis Verification Code")
	}
	if !strings.Contains(sender.body, "123456") {
		t.Errorf("body does not contain OTP: %q", sender.body)
	}
}

func TestEmailServiceSend(t *testing.T) {
	sender := &recordingEmailSender{}
	svc := newEmailService("noreply@fundis.app", sender)
	if err := svc.Send(EmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    "Hello",
	}); err != nil {
		t.Fatalf("EmailService.Send returned error: %v", err)
	}

	if sender.to != "test@example.com" || sender.subject != "Test" || sender.body != "Hello" {
		t.Fatalf("unexpected email: to=%q subject=%q body=%q", sender.to, sender.subject, sender.body)
	}
}
