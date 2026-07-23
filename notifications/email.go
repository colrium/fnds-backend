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
	body := fmt.Sprintf("Your OTP code is: %s\n\nThis code will expire in 5 minutes. Do not share it with anyone.", otp)
	return SendEmailViaSendGrid(context.Background(), email, "Your Fundis Verification Code", body)
}

// SendPasswordResetOTPEmail sends a password-reset specific OTP via email.
func SendPasswordResetOTPEmail(email, otp string) error {
	body := fmt.Sprintf(
		"Hello,\n\n"+
			"We received a request to reset your Fundis account password.\n\n"+
			"Your verification code is: %s\n\n"+
			"This code expires in 5 minutes. If you did not request a password reset, please ignore this email.\n\n"+
			"Best regards,\nThe Fundis Team",
		otp,
	)
	return SendEmailViaSendGrid(context.Background(), email, "Password Reset Code - Fundis", body)
}

// SendPasswordResetConfirmationEmail notifies a user that their password was
// successfully reset (unauthenticated forgot-password flow).
func SendPasswordResetConfirmationEmail(email, name string) error {
	body := fmt.Sprintf(
		"Hello %s,\n\n"+
			"Your Fundis account password has been successfully reset.\n\n"+
			"If you did not perform this action, please contact our support team immediately.\n\n"+
			"Best regards,\nThe Fundis Team",
		name,
	)
	return SendEmailViaSendGrid(context.Background(), email, "Password Reset Successful - Fundis", body)
}

// SendPasswordChangedEmail notifies a user that their password was changed
// from within their authenticated session.
func SendPasswordChangedEmail(email, name string) error {
	body := fmt.Sprintf(
		"Hello %s,\n\n"+
			"Your Fundis account password was recently changed.\n\n"+
			"If you did not make this change, please reset your password immediately or contact our support team.\n\n"+
			"Best regards,\nThe Fundis Team",
		name,
	)
	return SendEmailViaSendGrid(context.Background(), email, "Password Changed - Fundis", body)
}
