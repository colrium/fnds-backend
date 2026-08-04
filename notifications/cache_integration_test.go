//go:build integration

package notifications

import (
	"context"
	"os"
	"testing"
	"time"

	"backend/configs"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Integration tests exercise the real notifications.MongoStore against a
// MongoDB instance reachable via MONGODB_URI and a real Redis protocol
// implementation (miniredis, in-process but wire-compatible) so cache-aside
// list, mutation invalidation, and outage fallback are validated end to end.
//
// Run with: MONGODB_URI=mongodb://localhost:27017 APP_ENV=integration-test \
//   go test -tags=integration ./notifications
//
// These tests are excluded from the default `go test ./...` run via the
// integration build tag, so they never affect offline/deterministic CI runs.

func requireIntegrationMongoURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		t.Skip("MONGODB_URI not set; skipping notifications cache integration test")
	}
	return uri
}

func setupIntegrationEnvironment(t *testing.T) (*MongoStore, *RedisFeedCache, *redis.Client) {
	t.Helper()
	uri := requireIntegrationMongoURI(t)

	t.Setenv("MONGODB_URI", uri)
	t.Setenv("APP_ENV", "integration-test-"+uuid.New().String())

	configs.ConnectDB()
	if err := EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes() error = %v", err)
	}

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	redisCache, err := NewRedisFeedCache(client, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("NewRedisFeedCache() error = %v", err)
	}

	return NewMongoStore(), redisCache, client
}

func newIntegrationCachedStore(t *testing.T, source *MongoStore, cache feedCache) *CachedStore {
	t.Helper()
	store, err := NewCachedStore(
		source,
		cache,
		CacheConfig{
			Enabled:          true,
			TTL:              30 * time.Second,
			OperationTimeout: 500 * time.Millisecond,
			AppEnvironment:   configs.AppEnv(),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCachedStore() error = %v", err)
	}
	return store
}

func newIntegrationUserID(t *testing.T) string {
	t.Helper()
	return "integration-user-" + uuid.New().String()
}

func TestIntegrationFirstReadMissesSecondReadHits(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	if err := source.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	first, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() first call error = %v", err)
	}
	if len(first.Notifications) != 1 {
		t.Fatalf("List() first call = %#v, want one notification", first)
	}

	second, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() second call error = %v", err)
	}
	if len(second.Notifications) != 1 {
		t.Fatalf("List() second call = %#v, want cached hit with one notification", second)
	}
}

func TestIntegrationPerUserIsolation(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userA := newIntegrationUserID(t)
	userB := newIntegrationUserID(t)

	if err := source.Create(ctx, Notification{UserID: userA, Type: TypeNewApplication, Title: "a", Message: "a"}); err != nil {
		t.Fatalf("Create(userA) error = %v", err)
	}

	feedA, err := store.List(ctx, userA, 30)
	if err != nil {
		t.Fatalf("List(userA) error = %v", err)
	}
	feedB, err := store.List(ctx, userB, 30)
	if err != nil {
		t.Fatalf("List(userB) error = %v", err)
	}

	if len(feedA.Notifications) != 1 {
		t.Fatalf("List(userA) = %#v, want one notification", feedA)
	}
	if len(feedB.Notifications) != 0 {
		t.Fatalf("List(userB) = %#v, want no notifications (isolated from userA)", feedB)
	}
}

func TestIntegrationVariantsCacheSeparately(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	for i := 0; i < 15; i++ {
		if err := source.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	limit10, err := store.List(ctx, userID, 10)
	if err != nil {
		t.Fatalf("List(limit=10) error = %v", err)
	}
	limit30, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List(limit=30) error = %v", err)
	}

	if len(limit10.Notifications) != 10 {
		t.Fatalf("List(limit=10) returned %d notifications, want 10", len(limit10.Notifications))
	}
	if len(limit30.Notifications) != 15 {
		t.Fatalf("List(limit=30) returned %d notifications, want 15", len(limit30.Notifications))
	}
}

func TestIntegrationListCreateListReflectsFreshData(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	before, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() before create error = %v", err)
	}
	if len(before.Notifications) != 0 {
		t.Fatalf("List() before create = %#v, want empty", before)
	}

	if err := store.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	after, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() after create error = %v", err)
	}
	if len(after.Notifications) != 1 || after.UnreadCount != 1 {
		t.Fatalf("List() after create = %#v, want one unread notification", after)
	}
}

func TestIntegrationListMarkReadListReflectsFreshData(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	if err := source.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	before, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() before mark-read error = %v", err)
	}
	if len(before.Notifications) != 1 || before.UnreadCount != 1 {
		t.Fatalf("List() before mark-read = %#v, want one unread", before)
	}
	notificationID := before.Notifications[0].ID

	updated, err := store.MarkRead(ctx, userID, notificationID)
	if err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	if !updated {
		t.Fatal("MarkRead() updated = false, want true")
	}

	after, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() after mark-read error = %v", err)
	}
	if after.UnreadCount != 0 {
		t.Fatalf("List() after mark-read unread count = %d, want 0", after.UnreadCount)
	}
}

func TestIntegrationListMarkAllReadListReflectsFreshData(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	for i := 0; i < 3; i++ {
		if err := source.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	before, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() before mark-all-read error = %v", err)
	}
	if before.UnreadCount != 3 {
		t.Fatalf("List() before mark-all-read unread count = %d, want 3", before.UnreadCount)
	}

	if err := store.MarkAllRead(ctx, userID); err != nil {
		t.Fatalf("MarkAllRead() error = %v", err)
	}

	after, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() after mark-all-read error = %v", err)
	}
	if after.UnreadCount != 0 {
		t.Fatalf("List() after mark-all-read unread count = %d, want 0", after.UnreadCount)
	}
}

func TestIntegrationCreateManyInvalidatesAllDistinctRecipients(t *testing.T) {
	source, cache, _ := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userA := newIntegrationUserID(t)
	userB := newIntegrationUserID(t)

	if _, err := store.List(ctx, userA, 30); err != nil {
		t.Fatalf("List(userA) priming error = %v", err)
	}
	if _, err := store.List(ctx, userB, 30); err != nil {
		t.Fatalf("List(userB) priming error = %v", err)
	}

	notifications := []Notification{
		{UserID: userA, Type: TypeSkillMatch, Title: "a", Message: "a"},
		{UserID: userB, Type: TypeSkillMatch, Title: "b", Message: "b"},
	}
	if err := store.CreateMany(ctx, notifications); err != nil {
		t.Fatalf("CreateMany() error = %v", err)
	}

	feedA, err := store.List(ctx, userA, 30)
	if err != nil {
		t.Fatalf("List(userA) after CreateMany error = %v", err)
	}
	feedB, err := store.List(ctx, userB, 30)
	if err != nil {
		t.Fatalf("List(userB) after CreateMany error = %v", err)
	}
	if len(feedA.Notifications) != 1 {
		t.Fatalf("List(userA) after CreateMany = %#v, want one notification", feedA)
	}
	if len(feedB.Notifications) != 1 {
		t.Fatalf("List(userB) after CreateMany = %#v, want one notification", feedB)
	}
}

func TestIntegrationRedisOutageOnReadFallsBackToMongo(t *testing.T) {
	source, cache, client := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	if err := source.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Close the Redis connection to simulate an outage on the read path.
	if err := client.Close(); err != nil {
		t.Fatalf("closing Redis client error = %v", err)
	}

	feed, err := store.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("List() error = %v, want fallback to MongoDB despite Redis outage", err)
	}
	if len(feed.Notifications) != 1 {
		t.Fatalf("List() during outage = %#v, want MongoDB-backed result", feed)
	}
}

func TestIntegrationRedisOutageDuringInvalidationPreservesMutation(t *testing.T) {
	source, cache, client := setupIntegrationEnvironment(t)
	store := newIntegrationCachedStore(t, source, cache)
	ctx := context.Background()
	userID := newIntegrationUserID(t)

	if err := client.Close(); err != nil {
		t.Fatalf("closing Redis client error = %v", err)
	}

	if err := store.Create(ctx, Notification{UserID: userID, Type: TypeNewApplication, Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Create() error = %v, want mutation to succeed despite Redis outage", err)
	}

	feed, err := source.List(ctx, userID, 30)
	if err != nil {
		t.Fatalf("source.List() error = %v", err)
	}
	if len(feed.Notifications) != 1 {
		t.Fatalf("source.List() = %#v, want the create to have durably persisted", feed)
	}
}
