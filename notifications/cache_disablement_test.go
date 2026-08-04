package notifications

import (
	"context"
	"reflect"
	"testing"
	"testing/quick"
	"time"
)

// TestPropertyRuntimeDisablement is Property 14: with caching disabled or
// Redis unconfigured, CachedStore.List behaves exactly like direct
// MongoStore-equivalent delegation — same result, same error, and no cache
// side effects — across arbitrary generated feeds and limits.
func TestPropertyRuntimeDisablement(t *testing.T) {
	t.Parallel()

	property := func(seed genFeed, rawLimit int16) bool {
		feed := seed.toFeed()
		limit := normalizeHandlerLimit(int64(rawLimit))

		directSource := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limitArg int64) (Feed, error) {
			return feed, nil
		}}
		disabledCache := newFakeFeedCache()
		disabledStore, err := NewCachedStore(directSource, disabledCache, CacheConfig{Enabled: false}, nil)
		if err != nil {
			t.Logf("NewCachedStore() (disabled) error = %v", err)
			return false
		}

		nilRedisSource := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limitArg int64) (Feed, error) {
			return feed, nil
		}}
		nilRedisStore, err := NewCachedStore(nilRedisSource, nil, CacheConfig{
			Enabled:          true,
			TTL:              30 * time.Second,
			OperationTimeout: 100 * time.Millisecond,
			AppEnvironment:   "test",
		}, nil)
		if err != nil {
			t.Logf("NewCachedStore() (nil redis) error = %v", err)
			return false
		}

		ctx := context.Background()
		disabledResult, disabledErr := disabledStore.List(ctx, "user-1", limit)
		nilRedisResult, nilRedisErr := nilRedisStore.List(ctx, "user-1", limit)

		if disabledErr != nil || nilRedisErr != nil {
			t.Logf("unexpected errors: disabled=%v nilRedis=%v", disabledErr, nilRedisErr)
			return false
		}
		if !reflect.DeepEqual(disabledResult, feed) || !reflect.DeepEqual(nilRedisResult, feed) {
			t.Logf("results diverged from direct source: disabled=%#v nilRedis=%#v want=%#v", disabledResult, nilRedisResult, feed)
			return false
		}
		if directSource.listCalls != 1 || nilRedisSource.listCalls != 1 {
			t.Logf("source call counts: disabled=%d nilRedis=%d, want 1 each", directSource.listCalls, nilRedisSource.listCalls)
			return false
		}
		if len(disabledCache.values) != 0 {
			t.Logf("disabled cache unexpectedly populated: %v", disabledCache.values)
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatalf("Property 14 (runtime disablement) failed: %v", err)
	}
}

func TestPropertyRuntimeDisablementAppliesToMutationsToo(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, CacheConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.Create(context.Background(), Notification{UserID: "user-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.MarkAllRead(context.Background(), "user-1"); err != nil {
		t.Fatalf("MarkAllRead() error = %v", err)
	}
	if len(cache.generations) != 0 {
		t.Fatalf("generations = %v, want untouched while cache disabled", cache.generations)
	}
}

// TestColdCacheAndFlushBecomeOrdinaryMisses verifies Redis starting cold, or
// being flushed mid-operation, requires no prewarming or migration: it is
// simply treated as an ordinary cache miss that repopulates normally.
func TestColdCacheAndFlushBecomeOrdinaryMisses(t *testing.T) {
	t.Parallel()

	feed := Feed{Notifications: []Notification{{ID: "n1"}}, UnreadCount: 1}
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	// Cold start: first request is a miss, populates the cache.
	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() cold-start error = %v", err)
	}
	if source.listCalls != 1 {
		t.Fatalf("source.List() calls = %d, want 1 after cold start", source.listCalls)
	}

	// Simulate a Redis flush: clear all values (generations stay, mirroring a
	// FLUSHDB that wipes both, but a flush of feed entries alone is the more
	// common case and still must be handled as a miss).
	cache.values = make(map[string][]byte)

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() post-flush error = %v", err)
	}
	if source.listCalls != 2 {
		t.Fatalf("source.List() calls = %d, want 2 (flush repopulates via ordinary miss)", source.listCalls)
	}
}

// TestOldGenerationEntriesNeverServedAndRequireNoScan verifies that after a
// generation bump, an old-generation entry is never returned even though it
// still physically exists — no SCAN or synchronous cleanup runs on the
// request path; the entry simply becomes unreachable until TTL eviction.
func TestOldGenerationEntriesNeverServedAndRequireNoScan(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	oldGeneration := readGeneration(t, cache, "user-1")
	oldKey, err := feedKey("test", "user-1", oldGeneration, 30)
	if err != nil {
		t.Fatalf("feedKey() error = %v", err)
	}
	staleFeed := Feed{Notifications: []Notification{{ID: "stale"}}, UnreadCount: 99}
	stalePayload, err := encodeFeedCacheEnvelope(staleFeed)
	if err != nil {
		t.Fatalf("encodeFeedCacheEnvelope() error = %v", err)
	}
	cache.values[oldKey] = stalePayload

	if err := store.MarkAllRead(context.Background(), "user-1"); err != nil {
		t.Fatalf("MarkAllRead() error = %v", err)
	}

	freshFeed := Feed{Notifications: []Notification{}, UnreadCount: 0}
	source.listFn = func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return freshFeed, nil
	}

	got, err := store.List(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got.Notifications) != 0 {
		t.Fatalf("List() = %#v, want the fresh post-invalidation feed, not the old-generation stale entry", got)
	}

	// The stale entry is still physically present (TTL, not a scan, reaps
	// it) but was never read — confirming no key scan occurred.
	if _, exists := cache.values[oldKey]; !exists {
		t.Fatalf("old-generation entry %q disappeared unexpectedly; a scan/cleanup path may have run", oldKey)
	}
}
