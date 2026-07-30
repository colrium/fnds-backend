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
	return newConfiguredEmailSender().Send(ctx, to, subject, body)
}

func newConfiguredEmailSender() emailSender {
	return integrations.NewSendGridClient(
		configs.SendGridAPIKey(),
		configs.SendGridFromEmail(),
		configs.SendGridFromName(),
	)
}

type EmailService struct {
	DefaultFrom string
	sender      emailSender
}

func NewEmailService(defaultFrom string) *EmailService {
	return newEmailService(defaultFrom, newConfiguredEmailSender())
}

func newEmailService(defaultFrom string, sender emailSender) *EmailService {
	return &EmailService{DefaultFrom: defaultFrom, sender: sender}
}

func (s *EmailService) Send(req EmailRequest) error {
	if req.From == "" {
		req.From = s.DefaultFrom
	}
	log.Printf("[EmailService] sending to %s: %s", req.To, req.Subject)
	return s.sender.Send(context.Background(), req.To, req.Subject, req.Body)
}

func SendWelcomeEmail(email, name string) error {
	return sendWelcomeEmail(newConfiguredEmailSender(), email, name)
}

func sendWelcomeEmail(sender emailSender, email, name string) error {
	body := fmt.Sprintf("Hello %s,\n\nWelcome to Fundis! We're excited to have you on board.\n\nBest regards,\nThe Fundis Team", name)
	return sender.Send(context.Background(), email, "Welcome to Fundis", body)
}

func SendOTPEmail(email, otp string) error {
	return sendOTPEmail(newConfiguredEmailSender(), email, otp)
}

func sendOTPEmail(sender emailSender, email, otp string) error {
	body := fmt.Sprintf("Your OTP code is: %s\n\nThis code will expire in 5 minutes. Do not share it with anyone.", otp)
	return sender.Send(context.Background(), email, "Your Fundis Verification Code", body)
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

// SendDuplicateRegistrationAlertEmail notifies an existing account holder that
// somebody attempted to register a new account using their email. Helps them
// spot both innocent mistakes ("did I already sign up?") and account probing.
func SendDuplicateRegistrationAlertEmail(email, name string) error {
	greeting := "Hello"
	if name != "" {
		greeting = "Hello " + name
	}
	body := fmt.Sprintf(
		"%s,\n\n"+
			"Someone just tried to create a Fundis account using this email address, "+
			"but an account already exists for it.\n\n"+
			"If this was you, you can sign in at https://app.fundis.co.ke/sign-in — "+
			"use \"Forgot Password\" if you don't remember your password.\n\n"+
			"If it wasn't you, no action is needed. Your account is safe and no changes "+
			"were made. We just wanted you to know.\n\n"+
			"Best regards,\nThe Fundis Team",
		greeting,
	)
	return SendEmailViaSendGrid(context.Background(), email, "Sign-up attempt on your Fundis account", body)
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
