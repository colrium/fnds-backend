package notifications

import (
	"fmt"
	"log"

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
	if req.From == "" {
		req.From = s.DefaultFrom
	}
	log.Printf("[EmailService] sending to %s: %s", req.To, req.Subject)
	return integrations.SendEmailWithPlunk(req.Body, req.To, req.Subject, req.From)
}

func SendWelcomeEmail(email, name string) error {
	body := fmt.Sprintf("Hello %s,\n\nWelcome to Fundis! We're excited to have you on board.\n\nBest regards,\nThe Fundis Team", name)
	return integrations.SendEmailWithPlunk(body, email, "Welcome to Fundis", "noreply@fundis.app")
}

func SendOTPEmail(email, otp string) error {
	body := fmt.Sprintf("Your OTP code is: %s\n\nThis code will expire in 10 minutes.", otp)
	return integrations.SendEmailWithPlunk(body, email, "Your OTP Code", "noreply@fundis.app")
}
