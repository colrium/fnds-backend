package utils

import (
	"backend/notifications"
	"context"
	"fmt"
)

func SendOtpVerificationEmail(code, email, userId string) error {
	payload := fmt.Sprintf("Hello, \n\nYour OTP code is %s. \n\nPlease use this code to verify your email address. \n\nThank you.", code)
	err := notifications.SendEmailViaSendGrid(context.Background(), email, "Welcome to Fundis", payload)
	return err
}
