package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
)

func newHandlerTestContext(method, target, userID string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(req, recorder)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}
	return ctx, recorder
}

func TestHandleListUnauthorized(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodGet, "/notifications", "")

	if err := handler.HandleList(ctx); err != nil {
		t.Fatalf("HandleList() error = %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if store.listCalls != 0 {
		t.Fatalf("source.List() calls = %d, want 0 before authentication", store.listCalls)
	}
}

func TestHandleListLimitNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawLimit  string
		wantLimit int64
	}{
		{name: "missing", rawLimit: "", wantLimit: 30},
		{name: "malformed", rawLimit: "abc", wantLimit: 30},
		{name: "below minimum", rawLimit: "0", wantLimit: 30},
		{name: "negative", rawLimit: "-5", wantLimit: 30},
		{name: "in range", rawLimit: "10", wantLimit: 10},
		{name: "exactly default", rawLimit: "30", wantLimit: 30},
		{name: "exactly maximum", rawLimit: "100", wantLimit: 100},
		{name: "above maximum", rawLimit: "500", wantLimit: 100},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var gotLimit int64
			store := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
				gotLimit = limit
				return Feed{Notifications: []Notification{}, UnreadCount: 0}, nil
			}}
			handler := NewHandler(store)

			target := "/notifications"
			if test.rawLimit != "" {
				target += "?limit=" + test.rawLimit
			}
			ctx, recorder := newHandlerTestContext(http.MethodGet, target, "user-1")

			if err := handler.HandleList(ctx); err != nil {
				t.Fatalf("HandleList() error = %v", err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if gotLimit != test.wantLimit {
				t.Fatalf("normalized limit = %d, want %d", gotLimit, test.wantLimit)
			}
		})
	}
}

func TestHandleListSourceError(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return Feed{}, errors.New("mongo down")
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodGet, "/notifications", "user-1")

	if err := handler.HandleList(ctx); err != nil {
		t.Fatalf("HandleList() error = %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

func TestHandleListSuccessResponseShape(t *testing.T) {
	t.Parallel()

	feed := Feed{
		Notifications: []Notification{{ID: "n1", UserID: "user-1", Message: "hi"}},
		UnreadCount:   1,
	}
	store := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodGet, "/notifications", "user-1")

	if err := handler.HandleList(ctx); err != nil {
		t.Fatalf("HandleList() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    Feed   `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is invalid JSON: %v", err)
	}
	if !body.Success {
		t.Fatalf("success = %v, want true", body.Success)
	}
	if body.Message != "Notifications retrieved successfully" {
		t.Fatalf("message = %q, want unchanged success message", body.Message)
	}
	if len(body.Data.Notifications) != 1 || body.Data.UnreadCount != 1 {
		t.Fatalf("data = %#v, want feed unchanged", body.Data)
	}
}

func TestHandleListHitAndSourceBackedResponsesAreByteEquivalent(t *testing.T) {
	t.Parallel()

	feed := Feed{
		Notifications: []Notification{{ID: "n1", UserID: "user-1", Message: "hi", Read: false}},
		UnreadCount:   1,
	}

	sourceStore := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	sourceHandler := NewHandler(sourceStore)
	sourceCtx, sourceRecorder := newHandlerTestContext(http.MethodGet, "/notifications", "user-1")
	if err := sourceHandler.HandleList(sourceCtx); err != nil {
		t.Fatalf("HandleList() (source-backed) error = %v", err)
	}

	cache := newFakeFeedCache()
	cachedSource := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	cachedStore, err := NewCachedStore(cachedSource, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	// Prime the cache with a first request so the second is a cache hit.
	if _, err := cachedStore.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("priming List() error = %v", err)
	}
	hitHandler := NewHandler(cachedStore)
	hitCtx, hitRecorder := newHandlerTestContext(http.MethodGet, "/notifications", "user-1")
	if err := hitHandler.HandleList(hitCtx); err != nil {
		t.Fatalf("HandleList() (hit-backed) error = %v", err)
	}

	if sourceRecorder.Code != hitRecorder.Code {
		t.Fatalf("status mismatch: source=%d hit=%d", sourceRecorder.Code, hitRecorder.Code)
	}
	if sourceRecorder.Body.String() != hitRecorder.Body.String() {
		t.Fatalf("body mismatch:\nsource=%s\nhit=%s", sourceRecorder.Body.String(), hitRecorder.Body.String())
	}
}

func TestHandleMarkReadUnauthorized(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/n1/read", "")
	ctx.SetParamNames("id")
	ctx.SetParamValues("n1")

	if err := handler.HandleMarkRead(ctx); err != nil {
		t.Fatalf("HandleMarkRead() error = %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestHandleMarkReadEmptyNotificationID(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications//read", "user-1")
	ctx.SetParamNames("id")
	ctx.SetParamValues("")

	if err := handler.HandleMarkRead(ctx); err != nil {
		t.Fatalf("HandleMarkRead() error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHandleMarkReadNotFound(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{markReadFn: func(ctx context.Context, userID, notificationID string) (bool, error) {
		return false, nil
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/missing/read", "user-1")
	ctx.SetParamNames("id")
	ctx.SetParamValues("missing")

	if err := handler.HandleMarkRead(ctx); err != nil {
		t.Fatalf("HandleMarkRead() error = %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestHandleMarkReadSourceError(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{markReadFn: func(ctx context.Context, userID, notificationID string) (bool, error) {
		return false, errors.New("update failed")
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/n1/read", "user-1")
	ctx.SetParamNames("id")
	ctx.SetParamValues("n1")

	if err := handler.HandleMarkRead(ctx); err != nil {
		t.Fatalf("HandleMarkRead() error = %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

func TestHandleMarkReadSuccess(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{markReadFn: func(ctx context.Context, userID, notificationID string) (bool, error) {
		if userID != "user-1" || notificationID != "n1" {
			t.Fatalf("MarkRead() received (%q, %q)", userID, notificationID)
		}
		return true, nil
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/n1/read", "user-1")
	ctx.SetParamNames("id")
	ctx.SetParamValues("n1")

	if err := handler.HandleMarkRead(ctx); err != nil {
		t.Fatalf("HandleMarkRead() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

func TestHandleMarkAllReadUnauthorized(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/read-all", "")

	if err := handler.HandleMarkAllRead(ctx); err != nil {
		t.Fatalf("HandleMarkAllRead() error = %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestHandleMarkAllReadSourceError(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{markAllReadFn: func(ctx context.Context, userID string) error {
		return errors.New("update-many failed")
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/read-all", "user-1")

	if err := handler.HandleMarkAllRead(ctx); err != nil {
		t.Fatalf("HandleMarkAllRead() error = %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

func TestHandleMarkAllReadSuccess(t *testing.T) {
	t.Parallel()

	store := &fakeNotificationStore{markAllReadFn: func(ctx context.Context, userID string) error {
		if userID != "user-1" {
			t.Fatalf("MarkAllRead() received %q", userID)
		}
		return nil
	}}
	handler := NewHandler(store)
	ctx, recorder := newHandlerTestContext(http.MethodPatch, "/notifications/read-all", "user-1")

	if err := handler.HandleMarkAllRead(ctx); err != nil {
		t.Fatalf("HandleMarkAllRead() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}
