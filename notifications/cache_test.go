package notifications

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type cacheStoreStub struct{}

func (*cacheStoreStub) List(context.Context, string, int64) (Feed, error) {
	return Feed{}, nil
}

func (*cacheStoreStub) Create(context.Context, Notification) error {
	return nil
}

func (*cacheStoreStub) CreateMany(context.Context, []Notification) error {
	return nil
}

func (*cacheStoreStub) MarkRead(context.Context, string, string) (bool, error) {
	return true, nil
}

func (*cacheStoreStub) MarkAllRead(context.Context, string) error {
	return nil
}

func TestNewCachedStore(t *testing.T) {
	t.Parallel()

	validConfig := CacheConfig{
		Enabled:          true,
		TTL:              30 * time.Second,
		OperationTimeout: 100 * time.Millisecond,
		AppEnvironment:   "test",
	}

	tests := []struct {
		name      string
		source    notificationStore
		config    CacheConfig
		wantError bool
	}{
		{
			name:   "valid enabled configuration",
			source: &cacheStoreStub{},
			config: validConfig,
		},
		{
			name:   "disabled cache permits zero cache durations",
			source: &cacheStoreStub{},
			config: CacheConfig{},
		},
		{
			name:      "missing source",
			config:    validConfig,
			wantError: true,
		},
		{
			name:   "non-positive ttl when enabled",
			source: &cacheStoreStub{},
			config: CacheConfig{
				Enabled:          true,
				OperationTimeout: 100 * time.Millisecond,
			},
			wantError: true,
		},
		{
			name:   "non-positive operation timeout when enabled",
			source: &cacheStoreStub{},
			config: CacheConfig{
				Enabled: true,
				TTL:     30 * time.Second,
			},
			wantError: true,
		},
		{
			name:   "missing app environment when enabled",
			source: &cacheStoreStub{},
			config: CacheConfig{
				Enabled:          true,
				TTL:              30 * time.Second,
				OperationTimeout: 100 * time.Millisecond,
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewCachedStore(test.source, nil, test.config, nil)
			if test.wantError {
				if err == nil {
					t.Fatal("NewCachedStore() error = nil, want error")
				}
				if store != nil {
					t.Fatalf("NewCachedStore() store = %#v, want nil", store)
				}
				return
			}

			if err != nil {
				t.Fatalf("NewCachedStore() error = %v", err)
			}
			if store == nil {
				t.Fatal("NewCachedStore() store = nil")
			}
			if store.source != test.source {
				t.Fatal("NewCachedStore() did not retain the authoritative source")
			}
			if store.observer == nil {
				t.Fatal("NewCachedStore() observer = nil, want no-op observer")
			}
		})
	}
}

func TestNoopCacheObserver(t *testing.T) {
	t.Parallel()

	observer := noopCacheObserver{}
	observer.RecordLookup("miss", time.Millisecond)
	observer.RecordSourceLoad(time.Millisecond, "none")
	observer.RecordWrite("success")
	observer.RecordInvalidation("success", 1)
	observer.RecordCoalescedWait()
}

func TestCachedStoreDelegatesToAuthoritativeSource(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request-context")
	sourceErr := errors.New("source failure")
	notification := Notification{ID: "notification-1", UserID: "user-1"}
	notifications := []Notification{notification, {ID: "notification-2", UserID: "user-1"}}
	feed := Feed{Notifications: notifications}

	t.Run("List forwards arguments and result", func(t *testing.T) {
		source := &forwardingNotificationStore{
			list: func(got context.Context, userID string, limit int64) (Feed, error) {
				if got != ctx || userID != "user-1" || limit != 25 {
					t.Fatalf("List(%p, %q, %d) received unexpected arguments", got, userID, limit)
				}
				return feed, sourceErr
			},
		}
		store := mustCachedStore(t, source)
		got, err := store.List(ctx, "user-1", 25)
		if !errors.Is(err, sourceErr) || !reflect.DeepEqual(got, feed) {
			t.Errorf("List() = (%#v, %v), want (%#v, source error)", got, err, feed)
		}
	})

	t.Run("Create forwards arguments and error", func(t *testing.T) {
		source := &forwardingNotificationStore{
			create: func(got context.Context, value Notification) error {
				if got != ctx || !reflect.DeepEqual(value, notification) {
					t.Fatalf("Create() received (%p, %#v)", got, value)
				}
				return sourceErr
			},
		}
		if err := mustCachedStore(t, source).Create(ctx, notification); !errors.Is(err, sourceErr) {
			t.Errorf("Create() error = %v, want source error", err)
		}
	})

	t.Run("CreateMany forwards arguments and error", func(t *testing.T) {
		source := &forwardingNotificationStore{
			createMany: func(got context.Context, values []Notification) error {
				if got != ctx || !reflect.DeepEqual(values, notifications) {
					t.Fatalf("CreateMany() received (%p, %#v)", got, values)
				}
				return sourceErr
			},
		}
		if err := mustCachedStore(t, source).CreateMany(ctx, notifications); !errors.Is(err, sourceErr) {
			t.Errorf("CreateMany() error = %v, want source error", err)
		}
	})

	t.Run("MarkRead forwards arguments and result", func(t *testing.T) {
		source := &forwardingNotificationStore{
			markRead: func(got context.Context, userID, notificationID string) (bool, error) {
				if got != ctx || userID != "user-1" || notificationID != "notification-1" {
					t.Fatalf("MarkRead(%p, %q, %q) received unexpected arguments", got, userID, notificationID)
				}
				return true, sourceErr
			},
		}
		updated, err := mustCachedStore(t, source).MarkRead(ctx, "user-1", "notification-1")
		if !updated || !errors.Is(err, sourceErr) {
			t.Errorf("MarkRead() = (%v, %v), want (true, source error)", updated, err)
		}
	})

	t.Run("MarkAllRead forwards arguments and error", func(t *testing.T) {
		source := &forwardingNotificationStore{
			markAllRead: func(got context.Context, userID string) error {
				if got != ctx || userID != "user-1" {
					t.Fatalf("MarkAllRead(%p, %q) received unexpected arguments", got, userID)
				}
				return sourceErr
			},
		}
		if err := mustCachedStore(t, source).MarkAllRead(ctx, "user-1"); !errors.Is(err, sourceErr) {
			t.Errorf("MarkAllRead() error = %v, want source error", err)
		}
	})
}

type forwardingNotificationStore struct {
	list        func(context.Context, string, int64) (Feed, error)
	create      func(context.Context, Notification) error
	createMany  func(context.Context, []Notification) error
	markRead    func(context.Context, string, string) (bool, error)
	markAllRead func(context.Context, string) error
}

func (s *forwardingNotificationStore) List(ctx context.Context, userID string, limit int64) (Feed, error) {
	if s.list != nil {
		return s.list(ctx, userID, limit)
	}
	return Feed{}, nil
}

func (s *forwardingNotificationStore) Create(ctx context.Context, notification Notification) error {
	if s.create != nil {
		return s.create(ctx, notification)
	}
	return nil
}

func (s *forwardingNotificationStore) CreateMany(ctx context.Context, notifications []Notification) error {
	if s.createMany != nil {
		return s.createMany(ctx, notifications)
	}
	return nil
}

func (s *forwardingNotificationStore) MarkRead(ctx context.Context, userID, notificationID string) (bool, error) {
	if s.markRead != nil {
		return s.markRead(ctx, userID, notificationID)
	}
	return false, nil
}

func (s *forwardingNotificationStore) MarkAllRead(ctx context.Context, userID string) error {
	if s.markAllRead != nil {
		return s.markAllRead(ctx, userID)
	}
	return nil
}

func mustCachedStore(t *testing.T, source notificationStore) *CachedStore {
	t.Helper()
	store, err := NewCachedStore(source, nil, CacheConfig{}, nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	return store
}
