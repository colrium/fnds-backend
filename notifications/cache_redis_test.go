package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisFeedCache(t *testing.T, timeout time.Duration) (*RedisFeedCache, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cache, err := NewRedisFeedCache(client, timeout)
	if err != nil {
		t.Fatalf("NewRedisFeedCache() error = %v", err)
	}
	return cache, client
}

func TestNewRedisFeedCacheValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewRedisFeedCache(nil, time.Second); err == nil {
		t.Fatal("NewRedisFeedCache() error = nil, want error for nil commands client")
	}

	client := redis.NewClient(&redis.Options{})
	t.Cleanup(func() { _ = client.Close() })
	if _, err := NewRedisFeedCache(client, 0); err == nil {
		t.Fatal("NewRedisFeedCache() error = nil, want error for non-positive timeout")
	}
}

func TestRedisFeedCacheGenerationMissingIsZero(t *testing.T) {
	t.Parallel()
	cache, _ := newTestRedisFeedCache(t, time.Second)

	got, err := cache.Generation(context.Background(), "generation-key")
	if err != nil {
		t.Fatalf("Generation() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("Generation() = %d, want 0 for missing key", got)
	}
}

func TestRedisFeedCacheGetHitAndMiss(t *testing.T) {
	t.Parallel()
	cache, client := newTestRedisFeedCache(t, time.Second)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "missing-key"); !errors.Is(err, errCacheMiss) {
		t.Fatalf("Get() error = %v, want errCacheMiss", err)
	}

	if err := client.Set(ctx, "present-key", []byte("payload"), time.Minute).Err(); err != nil {
		t.Fatalf("seeding key failed: %v", err)
	}
	got, err := cache.Get(ctx, "present-key")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("Get() = %q, want %q", got, "payload")
	}
}

func TestRedisFeedCacheSetPositiveExpiry(t *testing.T) {
	t.Parallel()
	cache, client := newTestRedisFeedCache(t, time.Second)
	ctx := context.Background()

	if err := cache.Set(ctx, "feed-key", []byte("payload"), time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	ttl, err := client.TTL(ctx, "feed-key").Result()
	if err != nil {
		t.Fatalf("TTL() error = %v", err)
	}
	if ttl <= 0 {
		t.Fatalf("TTL() = %v, want positive expiry", ttl)
	}
}

func TestRedisFeedCacheSetRejectsNonPositiveTTL(t *testing.T) {
	t.Parallel()
	cache, _ := newTestRedisFeedCache(t, time.Second)

	if err := cache.Set(context.Background(), "feed-key", []byte("payload"), 0); err == nil {
		t.Fatal("Set() error = nil, want error for non-positive ttl")
	}
}

func TestRedisFeedCacheDeleteBestEffort(t *testing.T) {
	t.Parallel()
	cache, client := newTestRedisFeedCache(t, time.Second)
	ctx := context.Background()

	if err := client.Set(ctx, "to-delete", []byte("x"), time.Minute).Err(); err != nil {
		t.Fatalf("seeding key failed: %v", err)
	}
	if err := cache.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if exists, err := client.Exists(ctx, "to-delete").Result(); err != nil || exists != 0 {
		t.Fatalf("key still exists after Delete(): exists=%d err=%v", exists, err)
	}

	// Deleting an already-absent key is not an error.
	if err := cache.Delete(ctx, "already-gone"); err != nil {
		t.Fatalf("Delete() of missing key error = %v, want nil", err)
	}
}

func TestRedisFeedCacheBumpManyOnePerUniqueKey(t *testing.T) {
	t.Parallel()
	cache, client := newTestRedisFeedCache(t, time.Second)
	ctx := context.Background()

	keys := []string{"gen:user-1", "gen:user-2", "gen:user-1"}
	if err := cache.BumpMany(ctx, keys); err != nil {
		t.Fatalf("BumpMany() error = %v", err)
	}

	got1, err := client.Get(ctx, "gen:user-1").Int64()
	if err != nil {
		t.Fatalf("reading gen:user-1 error = %v", err)
	}
	if got1 != 2 {
		t.Fatalf("gen:user-1 = %d, want 2 (called twice by caller)", got1)
	}

	got2, err := client.Get(ctx, "gen:user-2").Int64()
	if err != nil {
		t.Fatalf("reading gen:user-2 error = %v", err)
	}
	if got2 != 1 {
		t.Fatalf("gen:user-2 = %d, want 1", got2)
	}
}

func TestRedisFeedCacheBumpManyEmptyIsNoop(t *testing.T) {
	t.Parallel()
	cache, _ := newTestRedisFeedCache(t, time.Second)

	if err := cache.BumpMany(context.Background(), nil); err != nil {
		t.Fatalf("BumpMany(nil) error = %v, want nil", err)
	}
	if err := cache.BumpMany(context.Background(), []string{}); err != nil {
		t.Fatalf("BumpMany([]) error = %v, want nil", err)
	}
}

func TestRedisFeedCacheGenerationKeysDoNotExpire(t *testing.T) {
	t.Parallel()
	cache, client := newTestRedisFeedCache(t, time.Second)
	ctx := context.Background()

	if err := cache.BumpMany(ctx, []string{"gen:persistent"}); err != nil {
		t.Fatalf("BumpMany() error = %v", err)
	}
	ttl, err := client.TTL(ctx, "gen:persistent").Result()
	if err != nil {
		t.Fatalf("TTL() error = %v", err)
	}
	if ttl != -1 {
		t.Fatalf("TTL() = %v, want -1 (no expiry) for generation key", ttl)
	}
}

func TestRedisFeedCacheCommandErrors(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisFeedCache(client, time.Second)
	if err != nil {
		t.Fatalf("NewRedisFeedCache() error = %v", err)
	}

	server.SetError("forced failure")
	ctx := context.Background()

	if _, err := cache.Get(ctx, "any-key"); err == nil || errors.Is(err, errCacheMiss) {
		t.Fatalf("Get() error = %v, want wrapped command error", err)
	}
	if err := cache.Set(ctx, "any-key", []byte("x"), time.Minute); err == nil {
		t.Fatal("Set() error = nil, want wrapped command error")
	}
	if err := cache.Delete(ctx, "any-key"); err == nil {
		t.Fatal("Delete() error = nil, want wrapped command error")
	}
	if _, err := cache.Generation(ctx, "any-key"); err == nil {
		t.Fatal("Generation() error = nil, want wrapped command error")
	}
	if err := cache.BumpMany(ctx, []string{"any-key"}); err == nil {
		t.Fatal("BumpMany() error = nil, want wrapped command error")
	}
}

func TestRedisFeedCacheCallerCancellation(t *testing.T) {
	t.Parallel()
	cache, _ := newTestRedisFeedCache(t, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cache.Get(ctx, "any-key"); err == nil {
		t.Fatal("Get() error = nil, want error for cancelled context")
	}
}

func TestRedisFeedCacheOperationTimeout(t *testing.T) {
	t.Parallel()
	cache, _ := newTestRedisFeedCache(t, time.Nanosecond)

	// A context.DeadlineExceeded surfaces through the wrapped command error
	// because the operation timeout is far tighter than any real command.
	_, err := cache.Get(context.Background(), "any-key")
	if err == nil {
		t.Fatal("Get() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get() error = %v, want context deadline exceeded", err)
	}
}
