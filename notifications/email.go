package notifications

import (
	"context"
	"fmt"
	"html"

	"backend/configs"
	"backend/integrations"
)

type EmailRequest struct {
	To      string
	Subject string
	Body    string
	From    string
}

type EmailService struct {
	DefaultFrom string
}

func NewEmailService(defaultFrom string) *EmailService {
	return &EmailService{DefaultFrom: defaultFrom}
}

func (s *EmailService) Send(req EmailRequest) error {
	from := req.From
	if from == "" {
		from = s.DefaultFrom
	}
	client := integrations.NewSendGridClient(
		configs.SendGridAPIKey(),
		from,
		configs.SendGridFromName(),
	)
	return client.Send(context.Background(), req.To, req.Subject, req.Body)
}

func SendWelcomeEmail(email, name string) error {
	body := fmt.Sprintf(
		"<p>Hello %s,</p><p>Welcome to Fundis. We're excited to have you on board.</p>",
		html.EscapeString(name),
	)
	return NewEmailService(configs.SendGridFromEmail()).Send(EmailRequest{
		To:      email,
		Subject: "Welcome to Fundis",
		Body:    body,
	})
}

func SendOTPEmail(email, otp string) error {
	body := fmt.Sprintf(
		"<p>Your OTP code is: <strong>%s</strong></p><p>This code will expire in 10 minutes.</p>",
		html.EscapeString(otp),
	)
	return NewEmailService(configs.SendGridFromEmail()).Send(EmailRequest{
		To:      email,
		Subject: "Your OTP Code",
		Body:    body,
	})
}
