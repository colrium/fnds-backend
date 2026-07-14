package notifications

import (
	"fmt"
	"log"

	"backend/integrations"
)

type SMSProvider string

const (
	SMSProviderAdvanta        SMSProvider = "advanta"
	SMSProviderAfricasTalking SMSProvider = "africastalking"
	SMSProviderTwilio         SMSProvider = "twilio"
)

type SMSClient interface {
	Send(to, message string) error
}

type AdvantaSMS struct{}

func (s *AdvantaSMS) Send(to, message string) error {
	return integrations.SendAdvantaSMS(to, message)
}

type AfricasTalkingSMS struct{}

func (s *AfricasTalkingSMS) Send(to, message string) error {
	log.Printf("[AfricasTalkingSMS] sending to %s: %s", to, message)
	return nil
}

type TwilioSMS struct{}

func (s *TwilioSMS) Send(to, message string) error {
	log.Printf("[TwilioSMS] sending to %s: %s", to, message)
	return nil
}

func NewSMSClient(provider SMSProvider) (SMSClient, error) {
	switch provider {
	case SMSProviderAdvanta:
		return &AdvantaSMS{}, nil
	case SMSProviderAfricasTalking:
		return &AfricasTalkingSMS{}, nil
	case SMSProviderTwilio:
		return &TwilioSMS{}, nil
	default:
		return nil, fmt.Errorf("unknown SMS provider: %s", provider)
	}
}

func SendSMS(provider SMSProvider, to, message string) error {
	client, err := NewSMSClient(provider)
	if err != nil {
		return err
	}
	return client.Send(to, message)
}

// DefaultSMSProvider is the provider used for transactional messages (OTP, etc.).
const DefaultSMSProvider = SMSProviderAdvanta

// SendOTPSMS sends a verification code to a phone number via the default provider.
func SendOTPSMS(to, otp string) error {
	message := fmt.Sprintf("Your Fundis verification code is %s. It expires shortly. Do not share it with anyone.", otp)
	return SendSMS(DefaultSMSProvider, to, message)
}
