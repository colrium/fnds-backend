package notifications

import (
	"context"
	"testing"
)

func TestNotifyProfileReviewDecisionApproved(t *testing.T) {
	stub := &notificationCreatorStub{}
	service := NewService(stub)

	err := service.NotifyProfileReviewDecision(context.Background(), ProfileReviewDecisionEvent{
		UserID:   "user-1",
		Approved: true,
	})
	if err != nil {
		t.Fatalf("NotifyProfileReviewDecision() error = %v", err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("created %d notifications, want 1", len(stub.created))
	}
	got := stub.created[0]
	if got.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", got.UserID)
	}
	if got.Type != TypeProfileReviewApproved {
		t.Errorf("Type = %q, want %q", got.Type, TypeProfileReviewApproved)
	}
}

func TestNotifyProfileReviewDecisionRejectedIncludesReason(t *testing.T) {
	stub := &notificationCreatorStub{}
	service := NewService(stub)

	err := service.NotifyProfileReviewDecision(context.Background(), ProfileReviewDecisionEvent{
		UserID:   "user-1",
		Approved: false,
		Reason:   "missing next of kin details",
	})
	if err != nil {
		t.Fatalf("NotifyProfileReviewDecision() error = %v", err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("created %d notifications, want 1", len(stub.created))
	}
	got := stub.created[0]
	if got.Type != TypeProfileReviewRejected {
		t.Errorf("Type = %q, want %q", got.Type, TypeProfileReviewRejected)
	}
	if got.Message == "" {
		t.Error("Message is empty, want the rejection reason included")
	}
}
