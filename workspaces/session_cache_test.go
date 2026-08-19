package workspaces

import (
	"context"
	"testing"
	"time"
)

// testClock is a controllable clock for cache tests.
type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestClock() *testClock {
	return &testClock{now: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func TestSessionCache_GetMiss(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	_, ok := cache.Get("user1", "ws1", 1)
	if ok {
		t.Fatal("expected cache miss for empty cache")
	}
}

func TestSessionCache_SetAndGet(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	sc := SessionContext{
		UserID:           "user1",
		WorkspaceID:      "ws1",
		AuthorityVersion: 5,
		Role:             RoleOwner,
	}
	cache.Set(sc)

	got, ok := cache.Get("user1", "ws1", 5)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.UserID != "user1" || got.WorkspaceID != "ws1" || got.AuthorityVersion != 5 {
		t.Fatalf("unexpected context: %+v", got)
	}
}

func TestSessionCache_TTLExpiry(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	sc := SessionContext{
		UserID:           "user1",
		WorkspaceID:      "ws1",
		AuthorityVersion: 1,
	}
	cache.Set(sc)

	// Still valid at 29 seconds.
	clock.Advance(29 * time.Second)
	if _, ok := cache.Get("user1", "ws1", 1); !ok {
		t.Fatal("expected cache hit before TTL")
	}

	// Expired at 31 seconds.
	clock.Advance(2 * time.Second)
	if _, ok := cache.Get("user1", "ws1", 1); ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestSessionCache_AuthorityVersionMismatch(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	sc := SessionContext{
		UserID:           "user1",
		WorkspaceID:      "ws1",
		AuthorityVersion: 3,
	}
	cache.Set(sc)

	// Same version — hit.
	if _, ok := cache.Get("user1", "ws1", 3); !ok {
		t.Fatal("expected cache hit with matching version")
	}

	// Different version — miss.
	if _, ok := cache.Get("user1", "ws1", 4); ok {
		t.Fatal("expected cache miss with newer authority version")
	}
}

func TestSessionCache_Invalidate(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	sc := SessionContext{
		UserID:           "user1",
		WorkspaceID:      "ws1",
		AuthorityVersion: 1,
	}
	cache.Set(sc)
	cache.Invalidate("user1", "ws1")

	if _, ok := cache.Get("user1", "ws1", 1); ok {
		t.Fatal("expected cache miss after invalidation")
	}
}

func TestSessionCache_InvalidateAll(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	cache.Set(SessionContext{UserID: "u1", WorkspaceID: "w1", AuthorityVersion: 1})
	cache.Set(SessionContext{UserID: "u2", WorkspaceID: "w2", AuthorityVersion: 1})

	if cache.Size() != 2 {
		t.Fatalf("expected 2 entries, got %d", cache.Size())
	}

	cache.InvalidateAll()

	if cache.Size() != 0 {
		t.Fatalf("expected 0 entries after InvalidateAll, got %d", cache.Size())
	}
}

func TestSessionCache_DefaultTTL(t *testing.T) {
	clock := newTestClock()
	// Pass zero TTL — should default to 30 seconds.
	cache := NewSessionCache(0, clock)

	sc := SessionContext{
		UserID:           "user1",
		WorkspaceID:      "ws1",
		AuthorityVersion: 1,
	}
	cache.Set(sc)

	clock.Advance(29 * time.Second)
	if _, ok := cache.Get("user1", "ws1", 1); !ok {
		t.Fatal("expected cache hit with default TTL at 29s")
	}

	clock.Advance(2 * time.Second)
	if _, ok := cache.Get("user1", "ws1", 1); ok {
		t.Fatal("expected cache miss with default TTL at 31s")
	}
}

func TestSharedSessionCache_LocalOnly(t *testing.T) {
	clock := newTestClock()
	// With configs.RDB == nil, should behave like local-only.
	shared := NewSharedSessionCache(30*time.Second, clock)

	sc := SessionContext{
		UserID:           "user1",
		WorkspaceID:      "ws1",
		AuthorityVersion: 2,
	}

	ctx := context.Background()
	shared.Set(ctx, sc)

	got, ok := shared.Get(ctx, "user1", "ws1", 2)
	if !ok {
		t.Fatal("expected cache hit from shared cache (local-only mode)")
	}
	if got.UserID != "user1" || got.AuthorityVersion != 2 {
		t.Fatalf("unexpected context: %+v", got)
	}

	// Invalidate.
	shared.Invalidate(ctx, "user1", "ws1")
	if _, ok := shared.Get(ctx, "user1", "ws1", 2); ok {
		t.Fatal("expected cache miss after invalidation")
	}
}

func TestSessionCache_MultipleEntries(t *testing.T) {
	clock := newTestClock()
	cache := NewSessionCache(30*time.Second, clock)

	sc1 := SessionContext{UserID: "u1", WorkspaceID: "w1", AuthorityVersion: 1, Role: RoleOwner}
	sc2 := SessionContext{UserID: "u1", WorkspaceID: "w2", AuthorityVersion: 3, Role: RoleAdmin}
	sc3 := SessionContext{UserID: "u2", WorkspaceID: "w1", AuthorityVersion: 2, Role: RoleHR}

	cache.Set(sc1)
	cache.Set(sc2)
	cache.Set(sc3)

	if cache.Size() != 3 {
		t.Fatalf("expected 3 entries, got %d", cache.Size())
	}

	// Each retrieves independently.
	if got, ok := cache.Get("u1", "w1", 1); !ok || got.Role != RoleOwner {
		t.Fatalf("unexpected u1/w1: ok=%v, got=%+v", ok, got)
	}
	if got, ok := cache.Get("u1", "w2", 3); !ok || got.Role != RoleAdmin {
		t.Fatalf("unexpected u1/w2: ok=%v, got=%+v", ok, got)
	}
	if got, ok := cache.Get("u2", "w1", 2); !ok || got.Role != RoleHR {
		t.Fatalf("unexpected u2/w1: ok=%v, got=%+v", ok, got)
	}

	// Invalidating one doesn't affect others.
	cache.Invalidate("u1", "w1")
	if _, ok := cache.Get("u1", "w1", 1); ok {
		t.Fatal("expected miss after invalidation of u1/w1")
	}
	if _, ok := cache.Get("u1", "w2", 3); !ok {
		t.Fatal("expected hit for u1/w2 after invalidating u1/w1")
	}
}
