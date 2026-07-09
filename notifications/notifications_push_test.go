package notifications

import (
	"testing"
)

func TestNewFCMService(t *testing.T) {
	svc := NewFCMService("test-key")
	if svc == nil {
		t.Fatal("expected non-nil FCMService")
	}
	if svc.ServerKey != "test-key" {
		t.Errorf("expected server key 'test-key', got %q", svc.ServerKey)
	}
}

func TestSendPushNotification(t *testing.T) {
	err := SendPushNotification("test-key", "fake-token", "Test Title", "Test Body")
	if err == nil {
		t.Log("SendPushNotification succeeded (may use invalid key)")
	}
}

func TestFCMServiceSendPush(t *testing.T) {
	svc := NewFCMService("test-key")
	err := svc.SendPush(PushNotificationRequest{
		Token: "fake-token",
		Title: "Test",
		Body:  "Body",
	})
	if err == nil {
		t.Log("FCMService.SendPush succeeded (may use invalid key)")
	}
}
