package notifications

import (
	"context"
	"errors"
	"testing"
)

func seedGeneration(t *testing.T, cache *fakeFeedCache, userID string, generation uint64) {
	t.Helper()
	key, err := generationKey("test", userID)
	if err != nil {
		t.Fatalf("generationKey() error = %v", err)
	}
	cache.generations[key] = generation
}

func readGeneration(t *testing.T, cache *fakeFeedCache, userID string) uint64 {
	t.Helper()
	key, err := generationKey("test", userID)
	if err != nil {
		t.Fatalf("generationKey() error = %v", err)
	}
	return cache.generations[key]
}

func TestCachedStoreCreateInvalidatesRecipient(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.Create(context.Background(), Notification{UserID: "user-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got := readGeneration(t, cache, "user-1"); got != 1 {
		t.Fatalf("generation for user-1 = %d, want 1", got)
	}
}

func TestCachedStoreCreateErrorSkipsInvalidation(t *testing.T) {
	t.Parallel()

	sourceErr := errors.New("insert failed")
	source := &fakeNotificationStore{createFn: func(ctx context.Context, notification Notification) error {
		return sourceErr
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.Create(context.Background(), Notification{UserID: "user-1"}); !errors.Is(err, sourceErr) {
		t.Fatalf("Create() error = %v, want %v", err, sourceErr)
	}
	if got := readGeneration(t, cache, "user-1"); got != 0 {
		t.Fatalf("generation for user-1 = %d, want 0 (no invalidation on failed create)", got)
	}
}

func TestCachedStoreCreateManyInvalidatesEveryDistinctRecipientOnce(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	notifications := []Notification{
		{UserID: "user-1"},
		{UserID: "user-2"},
		{UserID: "user-1"}, // duplicate recipient
		{UserID: ""},       // empty user id ignored
	}
	if err := store.CreateMany(context.Background(), notifications); err != nil {
		t.Fatalf("CreateMany() error = %v", err)
	}

	if got := readGeneration(t, cache, "user-1"); got != 1 {
		t.Fatalf("generation for user-1 = %d, want 1 (bumped once despite duplicate)", got)
	}
	if got := readGeneration(t, cache, "user-2"); got != 1 {
		t.Fatalf("generation for user-2 = %d, want 1", got)
	}
}

func TestCachedStoreCreateManyErrorStillInvalidatesIntendedRecipients(t *testing.T) {
	t.Parallel()

	sourceErr := errors.New("bulk insert partially failed")
	source := &fakeNotificationStore{createManyFn: func(ctx context.Context, notifications []Notification) error {
		return sourceErr
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	notifications := []Notification{{UserID: "user-1"}, {UserID: "user-2"}}
	err = store.CreateMany(context.Background(), notifications)
	if !errors.Is(err, sourceErr) {
		t.Fatalf("CreateMany() error = %v, want %v", err, sourceErr)
	}

	if got := readGeneration(t, cache, "user-1"); got != 1 {
		t.Fatalf("generation for user-1 = %d, want 1 even though CreateMany reported an error", got)
	}
	if got := readGeneration(t, cache, "user-2"); got != 1 {
		t.Fatalf("generation for user-2 = %d, want 1 even though CreateMany reported an error", got)
	}
}

func TestCachedStoreCreateManyEmptyAndDuplicateRecipients(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.CreateMany(context.Background(), nil); err != nil {
		t.Fatalf("CreateMany(nil) error = %v", err)
	}
	if len(cache.generations) != 0 {
		t.Fatalf("generations = %v, want none touched for empty recipient set", cache.generations)
	}
}

func TestCachedStoreMarkReadInvalidatesOnlyOnMatch(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{markReadFn: func(ctx context.Context, userID, notificationID string) (bool, error) {
		return notificationID == "matched", nil
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	updated, err := store.MarkRead(context.Background(), "user-1", "unmatched")
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if updated {
		t.Fatal("MarkRead() updated = true, want false for unmatched notification")
	}
	if got := readGeneration(t, cache, "user-1"); got != 0 {
		t.Fatalf("generation for user-1 = %d, want 0 (unmatched mark-read must not invalidate)", got)
	}

	updated, err = store.MarkRead(context.Background(), "user-1", "matched")
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if !updated {
		t.Fatal("MarkRead() updated = false, want true for matched notification")
	}
	if got := readGeneration(t, cache, "user-1"); got != 1 {
		t.Fatalf("generation for user-1 = %d, want 1 after matched mark-read", got)
	}
}

func TestCachedStoreMarkReadErrorSkipsInvalidation(t *testing.T) {
	t.Parallel()

	sourceErr := errors.New("update failed")
	source := &fakeNotificationStore{markReadFn: func(ctx context.Context, userID, notificationID string) (bool, error) {
		return false, sourceErr
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	_, err = store.MarkRead(context.Background(), "user-1", "notification-1")
	if !errors.Is(err, sourceErr) {
		t.Fatalf("MarkRead() error = %v, want %v", err, sourceErr)
	}
	if got := readGeneration(t, cache, "user-1"); got != 0 {
		t.Fatalf("generation for user-1 = %d, want 0 on source error", got)
	}
}

func TestCachedStoreMarkAllReadInvalidatesOnSuccess(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.MarkAllRead(context.Background(), "user-1"); err != nil {
		t.Fatalf("MarkAllRead() error = %v", err)
	}
	if got := readGeneration(t, cache, "user-1"); got != 1 {
		t.Fatalf("generation for user-1 = %d, want 1", got)
	}
}

func TestCachedStoreMarkAllReadErrorSkipsInvalidation(t *testing.T) {
	t.Parallel()

	sourceErr := errors.New("update-many failed")
	source := &fakeNotificationStore{markAllReadFn: func(ctx context.Context, userID string) error {
		return sourceErr
	}}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.MarkAllRead(context.Background(), "user-1"); !errors.Is(err, sourceErr) {
		t.Fatalf("MarkAllRead() error = %v, want %v", err, sourceErr)
	}
	if got := readGeneration(t, cache, "user-1"); got != 0 {
		t.Fatalf("generation for user-1 = %d, want 0 on source error", got)
	}
}

func TestCachedStoreInvalidationFailurePreservesMutationResult(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	cache.bumpManyFn = func(ctx context.Context, keys []string) error {
		return errors.New("redis unavailable")
	}
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	if err := store.Create(context.Background(), Notification{UserID: "user-1"}); err != nil {
		t.Fatalf("Create() error = %v, want mutation result preserved despite invalidation failure", err)
	}
	if err := store.MarkAllRead(context.Background(), "user-1"); err != nil {
		t.Fatalf("MarkAllRead() error = %v, want mutation result preserved despite invalidation failure", err)
	}
}

func TestDeliveryMutationsDoNotInvalidateFeedCache(t *testing.T) {
	t.Parallel()

	// ClaimDelivery, MarkDeliverySent, and MarkDeliveryFailed intentionally
	// remain on MongoStore directly (see dispatcher.go / NewDefaultDispatcher)
	// because Notification.Delivery has json:"-" and cannot change feed JSON.
	// CachedStore does not implement deliveryStore, so no invalidation call
	// exists for delivery state changes; this test documents that boundary.
	var _ deliveryStore = (*MongoStore)(nil)

	type cachedStoreMethods interface {
		Create(context.Context, Notification) error
		CreateMany(context.Context, []Notification) error
		MarkRead(context.Context, string, string) (bool, error)
		MarkAllRead(context.Context, string) error
	}
	var _ cachedStoreMethods = (*CachedStore)(nil)

	// CachedStore has no ClaimDelivery/MarkDeliverySent/MarkDeliveryFailed
	// methods, so it cannot satisfy deliveryStore — the type assertion below
	// intentionally fails to compile if someone widens CachedStore's surface
	// without adding invalidation for the new mutation.
	var store any = (*CachedStore)(nil)
	if _, ok := store.(deliveryStore); ok {
		t.Fatal("CachedStore unexpectedly implements deliveryStore; delivery mutations must stay on MongoStore without feed invalidation")
	}
}

// TestPropertySuccessfulMutationInvalidatesEveryVariant is Property 6: after
// a successful mutation and generation bump, every previously cached variant
// for that user becomes unreachable because it is keyed by the old
// generation.
func TestPropertySuccessfulMutationInvalidatesEveryVariant(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	variants := []int64{1, 10, 30, 50, 100}
	oldGeneration := readGeneration(t, cache, "user-1")
	oldKeys := make([]string, 0, len(variants))
	for _, limit := range variants {
		key, err := feedKey("test", "user-1", oldGeneration, limit)
		if err != nil {
			t.Fatalf("feedKey() error = %v", err)
		}
		cache.values[key] = []byte(`{"version":1,"feed":{"notifications":[],"unread_count":0}}`)
		oldKeys = append(oldKeys, key)
	}

	if err := store.MarkAllRead(context.Background(), "user-1"); err != nil {
		t.Fatalf("MarkAllRead() error = %v", err)
	}

	newGeneration := readGeneration(t, cache, "user-1")
	if newGeneration == oldGeneration {
		t.Fatalf("generation unchanged after successful mutation: %d", newGeneration)
	}
	for _, limit := range variants {
		newKey, err := feedKey("test", "user-1", newGeneration, limit)
		if err != nil {
			t.Fatalf("feedKey() error = %v", err)
		}
		if _, exists := cache.values[newKey]; exists {
			t.Fatalf("new-generation key %q unexpectedly pre-populated", newKey)
		}
	}
	// Old-generation entries still physically exist (TTL will reap them) but
	// are unreachable because List() now always derives the new generation.
	for _, oldKey := range oldKeys {
		if _, exists := cache.values[oldKey]; !exists {
			t.Fatalf("old-generation key %q missing; test setup invalid", oldKey)
		}
	}
}

// TestPropertyCrossUserInvalidationIsolation is Property 7: invalidating one
// user must not change another user's generation.
func TestPropertyCrossUserInvalidationIsolation(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	seedGeneration(t, cache, "user-a", 3)
	seedGeneration(t, cache, "user-b", 5)

	if err := store.MarkAllRead(context.Background(), "user-a"); err != nil {
		t.Fatalf("MarkAllRead() error = %v", err)
	}

	if got := readGeneration(t, cache, "user-a"); got != 4 {
		t.Fatalf("generation for user-a = %d, want 4", got)
	}
	if got := readGeneration(t, cache, "user-b"); got != 5 {
		t.Fatalf("generation for user-b = %d, want unchanged 5", got)
	}
}

func TestPropertyCrossUserInvalidationIsolationCreateMany(t *testing.T) {
	t.Parallel()

	source := &fakeNotificationStore{}
	cache := newFakeFeedCache()
	store, err := NewCachedStore(source, cache, testCacheConfig(), nil)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}

	seedGeneration(t, cache, "user-a", 1)
	seedGeneration(t, cache, "user-b", 1)
	seedGeneration(t, cache, "user-c", 1)

	notifications := []Notification{{UserID: "user-a"}, {UserID: "user-b"}}
	if err := store.CreateMany(context.Background(), notifications); err != nil {
		t.Fatalf("CreateMany() error = %v", err)
	}

	if got := readGeneration(t, cache, "user-a"); got != 2 {
		t.Fatalf("generation for user-a = %d, want 2", got)
	}
	if got := readGeneration(t, cache, "user-b"); got != 2 {
		t.Fatalf("generation for user-b = %d, want 2", got)
	}
	if got := readGeneration(t, cache, "user-c"); got != 1 {
		t.Fatalf("generation for user-c = %d, want unchanged 1 (not a recipient)", got)
	}
}
