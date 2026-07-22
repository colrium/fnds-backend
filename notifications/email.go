package notifications

import (
	"context"
	"fmt"
	"log"

	"backend/configs"
	"backend/integrations"
)

type EmailRequest struct {
	To      string
	Subject string
	Body    string
	From    string
}

// SendEmailViaSendGrid sends transactional mail through the configured
// provider and returns provider/API errors to the caller.
func SendEmailViaSendGrid(ctx context.Context, to, subject, body string) error {
	return integrations.NewSendGridClient(
		configs.SendGridAPIKey(),
		configs.SendGridFromEmail(),
		configs.SendGridFromName(),
	).Send(ctx, to, subject, body)
}

type EmailService struct {
	DefaultFrom string
}

func NewEmailService(defaultFrom string) *EmailService {
	return &EmailService{DefaultFrom: defaultFrom}
}

func (s *EmailService) Send(req EmailRequest) error {
	if req.From == "" {
		req.From = s.DefaultFrom
	}
	log.Printf("[EmailService] sending to %s: %s", req.To, req.Subject)
	return SendEmailViaSendGrid(context.Background(), req.To, req.Subject, req.Body)
}

func SendWelcomeEmail(email, name string) error {
	body := fmt.Sprintf("Hello %s,\n\nWelcome to Fundis! We're excited to have you on board.\n\nBest regards,\nThe Fundis Team", name)
	return SendEmailViaSendGrid(context.Background(), email, "Welcome to Fundis", body)
}

func SendOTPEmail(email, otp string) error {
	body := fmt.Sprintf("Your OTP code is: %s\n\nThis code will expire in 10 minutes.", otp)
	return SendEmailViaSendGrid(context.Background(), email, "Your OTP Code", body)
}
