package notifications

import (
	"fmt"
	"log"
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
	log.Printf("[AdvantaSMS] sending to %s: %s", to, message)
	return nil
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
