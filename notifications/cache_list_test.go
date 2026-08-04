package notifications

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFeedCache is a deterministic in-memory feedCache for unit tests. Each
// method can be overridden per-test to inject specific failures.
type fakeFeedCache struct {
	mu sync.Mutex

	values      map[string][]byte
	generations map[string]uint64

	getFn        func(ctx context.Context, key string) ([]byte, error)
	setFn        func(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	deleteFn     func(ctx context.Context, key string) error
	generationFn func(ctx context.Context, key string) (uint64, error)
	bumpManyFn   func(ctx context.Context, keys []string) error

	deletedKeys []string
	setCalls    int
}

func newFakeFeedCache() *fakeFeedCache {
	return &fakeFeedCache{
		values:      make(map[string][]byte),
		generations: make(map[string]uint64),
	}
}

func (f *fakeFeedCache) Get(ctx context.Context, key string) ([]byte, error) {
	if f.getFn != nil {
		return f.getFn(ctx, key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, ok := f.values[key]
	if !ok {
		return nil, errCacheMiss
	}
	return payload, nil
}

func (f *fakeFeedCache) Set(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	f.mu.Lock()
	f.setCalls++
	f.mu.Unlock()
	if f.setFn != nil {
		return f.setFn(ctx, key, payload, ttl)
	}
	if ttl <= 0 {
		return errors.New("fakeFeedCache: non-positive ttl")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = payload
	return nil
}

func (f *fakeFeedCache) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	f.deletedKeys = append(f.deletedKeys, key)
	f.mu.Unlock()
	if f.deleteFn != nil {
		return f.deleteFn(ctx, key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, key)
	return nil
}

func (f *fakeFeedCache) Generation(ctx context.Context, key string) (uint64, error) {
	if f.generationFn != nil {
		return f.generationFn(ctx, key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generations[key], nil
}

func (f *fakeFeedCache) BumpMany(ctx context.Context, keys []string) error {
	if f.bumpManyFn != nil {
		return f.bumpManyFn(ctx, keys)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range keys {
		f.generations[key]++
	}
	return nil
}

// fakeNotificationStore is a deterministic notificationStore fake with call
// counters, used to assert whether the source was invoked.
type fakeNotificationStore struct {
	listCalls int32
	listFn    func(ctx context.Context, userID string, limit int64) (Feed, error)

	createFn      func(ctx context.Context, notification Notification) error
	createManyFn  func(ctx context.Context, notifications []Notification) error
	markReadFn    func(ctx context.Context, userID, notificationID string) (bool, error)
	markAllReadFn func(ctx context.Context, userID string) error
}

func (f *fakeNotificationStore) List(ctx context.Context, userID string, limit int64) (Feed, error) {
	atomic.AddInt32(&f.listCalls, 1)
	if f.listFn != nil {
		return f.listFn(ctx, userID, limit)
	}
	return Feed{Notifications: []Notification{}, UnreadCount: 0}, nil
}

func (f *fakeNotificationStore) Create(ctx context.Context, notification Notification) error {
	if f.createFn != nil {
		return f.createFn(ctx, notification)
	}
	return nil
}

func (f *fakeNotificationStore) CreateMany(ctx context.Context, notifications []Notification) error {
	if f.createManyFn != nil {
		return f.createManyFn(ctx, notifications)
	}
	return nil
}

func (f *fakeNotificationStore) MarkRead(ctx context.Context, userID, notificationID string) (bool, error) {
	if f.markReadFn != nil {
		return f.markReadFn(ctx, userID, notificationID)
	}
	return true, nil
}

func (f *fakeNotificationStore) MarkAllRead(ctx context.Context, userID string) error {
	if f.markAllReadFn != nil {
		return f.markAllReadFn(ctx, userID)
	}
	return nil
}

func testCacheConfig() CacheConfig {
	return CacheConfig{
		Enabled:          true,
		TTL:              30 * time.Second,
		OperationTimeout: 100 * time.Millisecond,
		AppEnvironment:   "test",
	}
}

func TestCachedStoreListDisabledBypassesCache(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, CacheConfig{}, nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1", source.listCalls)
	}
	if len(cache.values) != 0 {
		t.Fatalf("cache.values = %v, want empty when disabled", cache.values)
	}
}

func TestCachedStoreListNilCacheBypasses(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	store, err := NewCachedStore(source, nil, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1", source.listCalls)
	}
}

func TestCachedStoreListMissThenFillThenHit(t *testing.T) {
	t.Parallel()

	feed := Feed{Notifications: []Notification{{ID: "n1", UserID: "user-1"}}, UnreadCount: 1}
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	got, err := store.List(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got.Notifications) != 1 || got.UnreadCount != 1 {
		t.Fatalf("List() = %#v, want feed with one notification", got)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1", source.listCalls)
	}

	// Second identical request should be a valid hit with no additional
	// source call.
	got2, err := store.List(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("List() second call error = %v", err)
	}
	if len(got2.Notifications) != 1 {
		t.Fatalf("List() second call = %#v, want cached feed", got2)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls after hit = %d, want still 1", source.listCalls)
	}
}

func TestCachedStoreListEmptyFeedIsCached(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return Feed{Notifications: []Notification{}, UnreadCount: 0}, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() second call error = %v", err)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1 (empty feed cached)", source.listCalls)
	}
}

func TestCachedStoreListInvalidEnvelopeDeletesAndReloads(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	genKey, err := generationKey("test", "user-1")
	if err != nil {
		t.Fatalf("generationKey() error = %v", err)
	}
	key, err := feedKey("test", "user-1", cache.generations[genKey], 30)
	if err != nil {
		t.Fatalf("feedKey() error = %v", err)
	}
	cache.values[key] = []byte("not valid json")

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1 after invalid entry", source.listCalls)
	}
	found := false
	for _, deleted := range cache.deletedKeys {
		if deleted == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("Delete() calls = %v, want invalid key %q deleted", cache.deletedKeys, key)
	}
}

func TestCachedStoreListGenerationReadErrorFallsOpen(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	cache.generationFn = func(ctx context.Context, key string) (uint64, error) {
		return 0, errors.New("redis down")
	}
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() error = %v, want source result despite cache failure", err)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1", source.listCalls)
	}
}

func TestCachedStoreListFeedReadErrorFallsOpen(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	cache.getFn = func(ctx context.Context, key string) ([]byte, error) {
		return nil, errors.New("redis timeout")
	}
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if _, err := store.List(context.Background(), "user-1", 30); err != nil {
		t.Fatalf("List() error = %v, want source result despite cache failure", err)
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1", source.listCalls)
	}
}

func TestCachedStoreListSetFailurePreservesSourceResult(t *testing.T) {
	t.Parallel()

	feed := Feed{Notifications: []Notification{{ID: "n1"}}, UnreadCount: 1}
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return feed, nil
	}}
	cache := newFakeFeedCache()
	cache.setFn = func(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
		return errors.New("redis write failed")
	}
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	got, err := store.List(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got.Notifications) != 1 {
		t.Fatalf("List() = %#v, want source feed despite write failure", got)
	}
}

func TestCachedStoreListMongoErrorReturnedUnchanged(t *testing.T) {
	t.Parallel()

	sourceErr := errors.New("mongo down")
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return Feed{}, sourceErr
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	_, err = store.List(context.Background(), "user-1", 30)
	if !errors.Is(err, sourceErr) {
		t.Fatalf("List() error = %v, want %v", err, sourceErr)
	}
	if len(cache.values) != 0 {
		t.Fatalf("cache.values = %v, want no cached error response", cache.values)
	}
}

func TestCachedStoreListCancelledContext(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	cache.generationFn = func(ctx context.Context, key string) (uint64, error) {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, nil
	}
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Cache read fails open; source.List must still run with the cancelled
	// context and its own behavior determines the outcome (fake ignores it).
	if _, err := store.List(ctx, "user-1", 30); err != nil {
		t.Fatalf("List() error = %v", err)
	}
}

// TestPropertyValidHitAvoidsSource is Property 4: a valid cached feed is
// returned without any source call.
func TestPropertyValidHitAvoidsSource(t *testing.T) {
	t.Parallel()

	feed := Feed{Notifications: []Notification{{ID: "n1"}}, UnreadCount: 1}
	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	genKey, err := generationKey("test", "user-1")
	if err != nil {
		t.Fatalf("generationKey() error = %v", err)
	}
	key, err := feedKey("test", "user-1", cache.generations[genKey], 30)
	if err != nil {
		t.Fatalf("feedKey() error = %v", err)
	}
	payload, err := encodeFeedCacheEnvelope(feed)
	if err != nil {
		t.Fatalf("encodeFeedCacheEnvelope() error = %v", err)
	}
	cache.values[key] = payload

	got, err := store.List(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got.Notifications) != 1 {
		t.Fatalf("List() = %#v, want cached feed", got)
	}
	if atomic.LoadInt32(&source.listCalls) != 0 {
		t.Fatalf("source.List() calls = %d, want 0 for a valid hit", source.listCalls)
	}
}

// TestPropertyCacheFailureTransparency is Property 5: for every cache
// operation failure point, if the source succeeds, its result is returned
// unchanged and no HTTP-relevant error is fabricated by the cache layer.
func TestPropertyCacheFailureTransparency(t *testing.T) {
	t.Parallel()

	feed := Feed{Notifications: []Notification{{ID: "n1"}}, UnreadCount: 7}

	failures := []struct {
		name   string
		mutate func(*fakeFeedCache)
	}{
		{name: "generation read fails", mutate: func(c *fakeFeedCache) {
			c.generationFn = func(ctx context.Context, key string) (uint64, error) {
				return 0, errors.New("boom")
			}
		}},
		{name: "feed get fails", mutate: func(c *fakeFeedCache) {
			c.getFn = func(ctx context.Context, key string) ([]byte, error) {
				return nil, errors.New("boom")
			}
		}},
		{name: "feed set fails", mutate: func(c *fakeFeedCache) {
			c.setFn = func(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
				return errors.New("boom")
			}
		}},
	}

	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			t.Parallel()

			source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
				return feed, nil
			}}
			cache := newFakeFeedCache()
			failure.mutate(cache)
			store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
			if err != nil {
				t.Fatalf("NewCachedStore() error = %v", err)
			}

			got, err := store.List(context.Background(), "user-1", 30)
			if err != nil {
				t.Fatalf("List() error = %v, want source result preserved", err)
			}
			if len(got.Notifications) != 1 || got.UnreadCount != 7 {
				t.Fatalf("List() = %#v, want source feed preserved", got)
			}
		})
	}
}

// TestPropertyBoundedCacheLifetime is Property 11: every fill uses a positive
// expiry within the configured jitter interval.
func TestPropertyBoundedCacheLifetime(t *testing.T) {
	t.Parallel()

	ttl := 30 * time.Second
	lowerBound := ttl - ttl*feedCacheTTLJitterPercent/100
	upperBound := ttl + ttl*feedCacheTTLJitterPercent/100

	for i := 0; i < 200; i++ {
		got := jitteredTTL(ttl)
		if got <= 0 {
			t.Fatalf("jitteredTTL() = %v, want positive", got)
		}
		if got < lowerBound || got > upperBound {
			t.Fatalf("jitteredTTL() = %v, want within [%v, %v]", got, lowerBound, upperBound)
		}
	}
}

// TestPropertyEmptyFeedCaching is Property 12: an empty feed is cached and
// reused across repeated requests until invalidated.
func TestPropertyEmptyFeedCaching(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		return Feed{Notifications: []Notification{}, UnreadCount: 0}, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		got, err := store.List(context.Background(), "user-1", 30)
		if err != nil {
			t.Fatalf("List() call %d error = %v", i, err)
		}
		if got.Notifications == nil || len(got.Notifications) != 0 {
			t.Fatalf("List() call %d = %#v, want initialized empty feed", i, got)
		}
	}
	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want 1 (empty feed reused from cache)", source.listCalls)
	}
}

// TestPropertyStampedeCoalescing is Property 10: concurrent same-key list
// calls against an empty cache execute at most one source load, while
// distinct users/variants never coalesce together.
func TestPropertyStampedeCoalescing(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var inFlight int32
	var maxInFlight int32
	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if current <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, current) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		return Feed{Notifications: []Notification{}, UnreadCount: 0}, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	const concurrentCallers = 10
	var wg sync.WaitGroup
	wg.Add(concurrentCallers)
	for i := 0; i < concurrentCallers; i++ {
		go func() {
			defer wg.Done()
			if _, err := store.List(context.Background(), "user-1", 30); err != nil {
				t.Errorf("List() error = %v", err)
			}
		}()
	}

	// Give every goroutine a chance to reach the singleflight call before
	// releasing the shared source load.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if atomic.LoadInt32(&source.listCalls) != 1 {
		t.Fatalf("source.List() calls = %d, want exactly 1 for coalesced same-key callers", source.listCalls)
	}
}

func TestPropertyStampedeCoalescingSeparatesDistinctKeys(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	var wg sync.WaitGroup
	users := []string{"user-1", "user-2", "user-3"}
	wg.Add(len(users))
	for _, userID := range users {
		userID := userID
		go func() {
			defer wg.Done()
			if _, err := store.List(context.Background(), userID, 30); err != nil {
				t.Errorf("List(%q) error = %v", userID, err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&source.listCalls); got != int32(len(users)) {
		t.Fatalf("source.List() calls = %d, want %d (one per distinct user)", got, len(users))
	}
}

// TestPropertyStaleFillCannotPoisonActiveGeneration is Property 8: a slow
// pre-mutation load must not write into the cache once the generation has
// advanced during that load.
func TestPropertyStaleFillCannotPoisonActiveGeneration(t *testing.T) {
	t.Parallel()

	loadStarted := make(chan struct{})
	proceedWithLoad := make(chan struct{})
	staleFeed := Feed{Notifications: []Notification{{ID: "stale"}}, UnreadCount: 99}

	source := &fakeNotificationStore{listFn: func(ctx context.Context, userID string, limit int64) (Feed, error) {
		close(loadStarted)
		<-proceedWithLoad
		return staleFeed, nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	done := make(chan Feed, 1)
	go func() {
		feed, err := store.List(context.Background(), "user-1", 30)
		if err != nil {
			t.Errorf("List() error = %v", err)
		}
		done <- feed
	}()

	<-loadStarted
	genKey, err := generationKey("test", "user-1")
	if err != nil {
		t.Fatalf("generationKey() error = %v", err)
	}
	// Simulate a concurrent mutation advancing the generation while the
	// source load for the pre-mutation generation is still in flight.
	if err := cache.BumpMany(context.Background(), []string{genKey}); err != nil {
		t.Fatalf("BumpMany() error = %v", err)
	}
	close(proceedWithLoad)

	got := <-done
	if len(got.Notifications) != 1 || got.Notifications[0].ID != "stale" {
		t.Fatalf("List() = %#v, want the loaded (stale) result returned to this caller", got)
	}

	newGeneration := cache.generations[genKey]
	newKey, err := feedKey("test", "user-1", newGeneration, 30)
	if err != nil {
		t.Fatalf("feedKey() error = %v", err)
	}
	if _, exists := cache.values[newKey]; exists {
		t.Fatalf("cache wrote stale result under the active generation key %q", newKey)
	}
}
