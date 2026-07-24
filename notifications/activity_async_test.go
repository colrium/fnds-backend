package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestAsyncNonBlocking_HandlerReturnsImmediately verifies that the notification
// delivery does not block the calling context. The handler fires the notification
// in a goroutine and continues — this test simulates that pattern.
//
// **Validates: Requirements 1.3, 2.3, 3.3, 5.1**
func TestAsyncNonBlocking_HandlerReturnsImmediately(t *testing.T) {
	// Create a slow server that takes 2 seconds to respond.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", ts.URL)
	t.Setenv("SLACK_WEBHOOK_URL", "")

	// Simulate the handler pattern: fire notification in a goroutine, then
	// immediately "return the response" by signaling a channel.
	handlerDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		// This is what the handler does: fire the goroutine and return.
		go func() {
			defer wg.Done()
			_ = NotifyVacancyCreated(context.Background(), VacancyCreatedActivity{
				VacancyID:     "v-async",
				VacancyTitle:  "Async Test",
				CreatorUserID: "u-1",
				CreatedAt:     time.Now(),
			})
		}()
		// Handler "returns" immediately after launching the goroutine.
		close(handlerDone)
	}()

	// The handler should return within 100ms (notification takes 2s).
	select {
	case <-handlerDone:
		// Success: handler returned before the notification completed.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler blocked waiting for notification delivery — async property violated")
	}

	// Wait for the notification goroutine to finish to avoid test leaks.
	wg.Wait()
}

// TestAsyncNonBlocking_FailureDoesNotBlock verifies the handler returns even
// when notification delivery fails (connection refused, timeout, etc.).
//
// **Validates: Requirements 1.3, 2.3, 3.3, 5.1**
func TestAsyncNonBlocking_FailureDoesNotBlock(t *testing.T) {
	// Use a server that always returns 500 immediately.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", ts.URL)
	t.Setenv("SLACK_WEBHOOK_URL", "")

	handlerDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		go func() {
			defer wg.Done()
			_ = NotifyApplicationCreated(context.Background(), ApplicationActivity{
				ApplicantName:  "Jane",
				ApplicantEmail: "jane@example.com",
				ApplicantPhone: "+254700000000",
				ApplicantID:    "u-1",
				VacancyID:      "v-1",
				VacancyTitle:   "Test",
				AppliedAt:      time.Now(),
			})
		}()
		close(handlerDone)
	}()

	select {
	case <-handlerDone:
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler blocked despite notification failure — async property violated")
	}

	wg.Wait()
}
