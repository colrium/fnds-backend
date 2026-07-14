package notifications

import (
	"testing"
)

func TestNewSMSClientAdvanta(t *testing.T) {
	client, err := NewSMSClient(SMSProviderAdvanta)
	if err != nil {
		t.Fatalf("NewSMSClient(advanta) returned error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	_, ok := client.(*AdvantaSMS)
	if !ok {
		t.Error("expected AdvantaSMS client")
	}
}

func TestNewSMSClientAfricasTalking(t *testing.T) {
	client, err := NewSMSClient(SMSProviderAfricasTalking)
	if err != nil {
		t.Fatalf("NewSMSClient(africastalking) returned error: %v", err)
	}
	_, ok := client.(*AfricasTalkingSMS)
	if !ok {
		t.Error("expected AfricasTalkingSMS client")
	}
}

func TestNewSMSClientTwilio(t *testing.T) {
	client, err := NewSMSClient(SMSProviderTwilio)
	if err != nil {
		t.Fatalf("NewSMSClient(twilio) returned error: %v", err)
	}
	_, ok := client.(*TwilioSMS)
	if !ok {
		t.Error("expected TwilioSMS client")
	}
}

func TestNewSMSClientUnknownProvider(t *testing.T) {
	client, err := NewSMSClient("unknown")
	if err == nil {
		t.Error("expected error for unknown provider, got nil")
	}
	if client != nil {
		t.Error("expected nil client for unknown provider")
	}
}

func TestSendSMSInvalidProvider(t *testing.T) {
	err := SendSMS("invalid", "+250700000000", "Test message")
	if err == nil {
		t.Error("expected error for invalid provider, got nil")
	}
}

func TestAdvantaSMSSendUnconfigured(t *testing.T) {
	// Without ADVANTA_* credentials, Advanta must refuse rather than pretend success.
	t.Setenv("ADVANTA_API_KEY", "")
	t.Setenv("ADVANTA_PARTNER_ID", "")
	t.Setenv("ADVANTA_SHORTCODE", "")
	sms := &AdvantaSMS{}
	err := sms.Send("+254700000000", "Hello from Advanta")
	if err == nil {
		t.Error("expected error when Advanta is not configured, got nil")
	}
}

func TestAfricasTalkingSMSSend(t *testing.T) {
	sms := &AfricasTalkingSMS{}
	err := sms.Send("+250700000000", "Hello from AfricasTalking")
	if err != nil {
		t.Errorf("AfricasTalkingSMS.Send() returned error: %v", err)
	}
}

func TestTwilioSMSSend(t *testing.T) {
	sms := &TwilioSMS{}
	err := sms.Send("+250700000000", "Hello from Twilio")
	if err != nil {
		t.Errorf("TwilioSMS.Send() returned error: %v", err)
	}
}
