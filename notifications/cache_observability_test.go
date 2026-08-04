package notifications

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// capturingCacheObserver records every argument passed to it so tests can
// assert bounded-cardinality outcomes and the absence of sensitive values.
type capturingCacheObserver struct {
	lookupOutcomes         []string
	sourceLoadErrorClasses []string
	writeOutcomes          []string
	invalidationOutcomes   []string
	coalescedWaits         int
}

func (o *capturingCacheObserver) RecordLookup(outcome string, _ time.Duration) {
	o.lookupOutcomes = append(o.lookupOutcomes, outcome)
}

func (o *capturingCacheObserver) RecordSourceLoad(_ time.Duration, errorClass string) {
	o.sourceLoadErrorClasses = append(o.sourceLoadErrorClasses, errorClass)
}

func (o *capturingCacheObserver) RecordWrite(outcome string) {
	o.writeOutcomes = append(o.writeOutcomes, outcome)
}

func (o *capturingCacheObserver) RecordInvalidation(outcome string, _ int) {
	o.invalidationOutcomes = append(o.invalidationOutcomes, outcome)
}

func (o *capturingCacheObserver) RecordCoalescedWait() {
	o.coalescedWaits++
}

func (o *capturingCacheObserver) allLabels() []string {
	var labels []string
	labels = append(labels, o.lookupOutcomes...)
	labels = append(labels, o.sourceLoadErrorClasses...)
	labels = append(labels, o.writeOutcomes...)
	labels = append(labels, o.invalidationOutcomes...)
	return labels
}

// TestPropertySensitiveDataSafeObservability is Property 13: cache outcomes
// and failure logs never contain raw user IDs, notification IDs, notification
// content, action URLs, Redis URLs, cache keys, or payloads, across every
// forced failure path.
func TestPropertySensitiveDataSafeObservability(t *testing.T) {
	const (
		markerUserID    = "SECRET-USER-ID-af019c"
		markerNotifID   = "SECRET-NOTIF-ID-9df213"
		markerTitle     = "SECRET-TITLE-1a2b3c"
		markerMessage   = "SECRET-MESSAGE-4d5e6f"
		markerActionURL = "/secret-action-url-7g8h9i"
		markerRedisURL  = "redis://user:SECRET-PASSWORD@example.test:6379/0"
	)

	var logBuf bytes.Buffer
	previousDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previousDefault) })

	feed := Feed{
		Notifications: []Notification{{
			ID:        markerNotifID,
			UserID:    markerUserID,
			Title:     markerTitle,
			Message:   markerMessage,
			ActionURL: markerActionURL,
		}},
		UnreadCount: 1,
	}

	failurePoints := []struct {
		name   string
		mutate func(*fakeFeedCache)
	}{
		{name: "generation read fails", mutate: func(c *fakeFeedCache) {
			c.generationFn = func(ctx context.Context, key string) (uint64, error) {
				return 0, errors.New("boom against " + markerRedisURL + " key " + key)
			}
		}},
		{name: "feed get fails", mutate: func(c *fakeFeedCache) {
			c.getFn = func(ctx context.Context, key string) ([]byte, error) {
				return nil, errors.New("boom against " + markerRedisURL + " key " + key)
			}
		}},
		{name: "feed set fails", mutate: func(c *fakeFeedCache) {
			c.setFn = func(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
				return errors.New("boom against " + markerRedisURL + " key " + key)
			}
		}},
		{name: "invalidation fails", mutate: func(c *fakeFeedCache) {
			c.bumpManyFn = func(ctx context.Context, keys []string) error {
				return errors.New("boom against " + markerRedisURL + " keys " + strings.Join(keys, ","))
			}
		}},
	}

	forbidden := []string{
		markerUserID, markerNotifID, markerTitle, markerMessage, markerActionURL, markerRedisURL,
	}

	for _, failure := range failurePoints {
		t.Run(failure.name, func(t *testing.T) {
			source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
				return feed, nil
			}}
			cache := newFakeFeedCache()
			failure.mutate(cache)
			observer := &capturingCacheObserver{}
			store, err := NewCachedStore(source, cache, testCacheConfig(), observer)
			if err != nil {
				t.Fatalf("NewCachedStore() error = %v", err)
			}

			logBuf.Reset()
			if _, err := store.List(context.Background(), markerUserID, 30); err != nil {
				t.Fatalf("List() error = %v", err)
			}
			store.invalidateUsers(context.Background(), []string{markerUserID})

			logOutput := logBuf.String()
			for _, marker := range forbidden {
				if strings.Contains(logOutput, marker) {
					t.Fatalf("log output leaked sensitive marker %q:\n%s", marker, logOutput)
				}
			}
			for _, label := range observer.allLabels() {
				for _, marker := range forbidden {
					if strings.Contains(label, marker) {
						t.Fatalf("observer label %q leaked sensitive marker %q", label, marker)
					}
				}
			}
		})
	}
}

// TestInvalidationLogsBoundedFields asserts invalidation warnings only ever
// carry an operation label, outcome/error class, and aggregate user count —
// never a per-user identifier.
func TestInvalidationLogsBoundedFields(t *testing.T) {
	var logBuf bytes.Buffer
	previousDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(previousDefault) })

	const markerUserID = "SECRET-USER-ID-xyz"
	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	cache.bumpManyFn = func(ctx context.Context, keys []string) error {
		return errors.New("redis unavailable")
	}
	observer := &capturingCacheObserver{}
	store, err := NewCachedStore(source, cache, testCacheConfig(), observer)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	store.invalidateUsers(context.Background(), []string{markerUserID})

	logOutput := logBuf.String()
	if strings.Contains(logOutput, markerUserID) {
		t.Fatalf("invalidation log leaked raw user ID: %s", logOutput)
	}
	if !strings.Contains(logOutput, "operation=invalidation") {
		t.Fatalf("invalidation log missing bounded operation label: %s", logOutput)
	}
	if len(observer.invalidationOutcomes) != 1 || observer.invalidationOutcomes[0] != "error" {
		t.Fatalf("invalidation outcomes = %v, want single bounded \"error\" outcome", observer.invalidationOutcomes)
	}
}
