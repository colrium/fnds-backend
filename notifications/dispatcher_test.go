package notifications

import (
	"context"
	"errors"
	"testing"
)

type deliveryStoreStub struct {
	notification *Notification
	sentID       string
	failedID     string
}

func (s *deliveryStoreStub) ClaimDelivery(context.Context) (*Notification, error) {
	notification := s.notification
	s.notification = nil
	return notification, nil
}

func (s *deliveryStoreStub) MarkDeliverySent(_ context.Context, id string) error {
	s.sentID = id
	return nil
}

func (s *deliveryStoreStub) MarkDeliveryFailed(_ context.Context, id string, _ error) error {
	s.failedID = id
	return nil
}

func TestDispatcherProcessOneSendsQueuedEmail(t *testing.T) {
	store := &deliveryStoreStub{notification: &Notification{
		ID: "notification-1",
		Delivery: &Delivery{
			Channel: DeliveryChannelEmail,
			To:      "employer@example.com",
			Subject: "New application",
			Body:    "Amina applied",
		},
	}}
	email := &emailSenderStub{}
	dispatcher := NewDispatcher(store, email, &smsSenderStub{})

	processed, err := dispatcher.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne returned error: %v", err)
	}
	if !processed || len(email.messages) != 1 {
		t.Fatalf("processed = %v, emails = %#v", processed, email.messages)
	}
	if store.sentID != "notification-1" {
		t.Fatalf("sent ID = %q", store.sentID)
	}
}

func TestDispatcherProcessOneRetriesProviderFailure(t *testing.T) {
	store := &deliveryStoreStub{notification: &Notification{
		ID: "notification-1",
		Delivery: &Delivery{
			Channel: DeliveryChannelSMS,
			To:      "254700000000",
			Body:    "You were hired",
		},
	}}
	dispatcher := NewDispatcher(
		store,
		&emailSenderStub{},
		&failingSMSSender{err: errors.New("provider unavailable")},
	)

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("processed = %v, err = %v", processed, err)
	}
	if store.failedID != "notification-1" {
		t.Fatalf("failed ID = %q", store.failedID)
	}
}

type failingSMSSender struct {
	err error
}

func (s *failingSMSSender) Send(string, string) error {
	return s.err
}
