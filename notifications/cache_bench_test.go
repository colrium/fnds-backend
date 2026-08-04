package notifications

import (
	"context"
	"testing"
	"time"
)

func BenchmarkFeedKey(b *testing.B) {
	for b.Loop() {
		if _, err := feedKey("production", "authenticated-user-42", 17, 30); err != nil {
			b.Fatalf("feedKey() error = %v", err)
		}
	}
}

func BenchmarkGenerationKey(b *testing.B) {
	for b.Loop() {
		if _, err := generationKey("production", "authenticated-user-42"); err != nil {
			b.Fatalf("generationKey() error = %v", err)
		}
	}
}

func BenchmarkEncodeFeedCacheEnvelope(b *testing.B) {
	feed := benchmarkFeed()
	for b.Loop() {
		if _, err := encodeFeedCacheEnvelope(feed); err != nil {
			b.Fatalf("encodeFeedCacheEnvelope() error = %v", err)
		}
	}
}

func BenchmarkDecodeFeedCacheEnvelope(b *testing.B) {
	feed := benchmarkFeed()
	payload, err := encodeFeedCacheEnvelope(feed)
	if err != nil {
		b.Fatalf("encodeFeedCacheEnvelope() error = %v", err)
	}

	for b.Loop() {
		if _, classification := decodeFeedCacheEnvelope(payload); classification != feedCacheDecodeValid {
			b.Fatalf("decodeFeedCacheEnvelope() classification = %v, want valid", classification)
		}
	}
}

// BenchmarkCachedStoreListWarmHit measures the cache-aside List path once an
// entry is already cached, to compare against the fake-source baseline
// below and quantify overhead added by cache lookup/decode.
func BenchmarkCachedStoreListWarmHit(b *testing.B) {
	feed := benchmarkFeed()
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, CacheConfig{
		Enabled:          true,
		TTL:              30 * time.Second,
		OperationTimeout: 100 * time.Millisecond,
		AppEnvironment:   "bench",
	}, nil)
	if err != nil {
		b.Fatalf("NewCachedStore() error = %v", err)
	}

	ctx := context.Background()
	if _, err := store.List(ctx, "bench-user", 30); err != nil {
		b.Fatalf("priming List() error = %v", err)
	}

	for b.Loop() {
		if _, err := store.List(ctx, "bench-user", 30); err != nil {
			b.Fatalf("List() error = %v", err)
		}
	}
}

// BenchmarkFakeSourceListBaseline measures the fake source directly, giving
// a baseline to compare BenchmarkCachedStoreListWarmHit's warm-hit overhead
// against.
func BenchmarkFakeSourceListBaseline(b *testing.B) {
	feed := benchmarkFeed()
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	ctx := context.Background()

	for b.Loop() {
		if _, err := source.List(ctx, "bench-user", 30); err != nil {
			b.Fatalf("List() error = %v", err)
		}
	}
}

func benchmarkFeed() Feed {
	notifications := make([]Notification, 30)
	for i := range notifications {
		notifications[i] = Notification{
			ID:        "notification-id",
			UserID:    "bench-user",
			Type:      TypeNewApplication,
			Title:     "Benchmark title",
			Message:   "Benchmark message body used to approximate realistic payload size.",
			ActionURL: "/applications/1",
			Read:      i%2 == 0,
			CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		}
	}
	return Feed{Notifications: notifications, UnreadCount: 15}
}
