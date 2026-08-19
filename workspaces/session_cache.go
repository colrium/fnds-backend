package workspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/configs"
	"backend/stytch"

	"github.com/redis/go-redis/v9"
)

// SessionCache provides a two-tier cache for resolved SessionContext values.
//
// Tier 1 (always present): in-process sharded map with 30-second TTL.
// Tier 2 (when Redis available): shared Redis hash, cutting the per-instance
//
//	Mongo read to one across the fleet (Deferred Item 3).
//
// The 30-second bound satisfies Req 17.8 regardless of whether Redis is
// available. Redis pub/sub invalidation (already implemented in
// MembershipService.invalidateAuthority) provides immediate cross-instance
// invalidation when Redis is present.
//
// Requirements: 16.7, 17.8
type SessionCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]*cacheEntry
	clock   stytch.Clock
	ttl     time.Duration
}

type cacheKey struct {
	UserID      string
	WorkspaceID string
}

type cacheEntry struct {
	Context          SessionContext `json:"context"`
	AuthorityVersion int64          `json:"authority_version"`
	ExpiresAt        time.Time      `json:"expires_at"`
}

// NewSessionCache creates a SessionCache with the given TTL and clock.
func NewSessionCache(ttl time.Duration, clock stytch.Clock) *SessionCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &SessionCache{
		entries: make(map[cacheKey]*cacheEntry),
		clock:   clock,
		ttl:     ttl,
	}
}

// Get retrieves a cached SessionContext if it exists, is not expired, and
// has a matching AuthorityVersion. Returns (context, true) on hit, or
// (zero, false) on miss.
//
// A hit means zero Mongo reads and zero Stytch calls (Req 16.9).
func (c *SessionCache) Get(userID, workspaceID string, currentVersion int64) (SessionContext, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := cacheKey{UserID: userID, WorkspaceID: workspaceID}
	entry, ok := c.entries[key]
	if !ok {
		return SessionContext{}, false
	}

	// Check TTL.
	if c.clock.Now().After(entry.ExpiresAt) {
		return SessionContext{}, false
	}

	// Check authority version hasn't changed (Req 16.7, 16.8).
	if entry.AuthorityVersion != currentVersion {
		return SessionContext{}, false
	}

	return entry.Context, true
}

// Set stores a resolved SessionContext in the cache.
func (c *SessionCache) Set(sc SessionContext) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey{UserID: sc.UserID, WorkspaceID: sc.WorkspaceID}
	c.entries[key] = &cacheEntry{
		Context:          sc,
		AuthorityVersion: sc.AuthorityVersion,
		ExpiresAt:        c.clock.Now().Add(c.ttl),
	}
}

// Invalidate removes the cached entry for the given user and workspace.
// Called by MembershipService after authority changes (Req 17.4).
func (c *SessionCache) Invalidate(userID, workspaceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, cacheKey{UserID: userID, WorkspaceID: workspaceID})
}

// InvalidateAll removes all cached entries. Used for testing and graceful shutdown.
func (c *SessionCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[cacheKey]*cacheEntry)
}

// Size returns the number of cached entries. Used for health/metrics reporting.
func (c *SessionCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// ─── Shared Redis store (Tier 2) ─────────────────────────────────────────────

// SharedSessionCache wraps the in-process cache with optional Redis backing.
// When Redis is available, Get checks Redis first (cutting Mongo reads across
// the fleet), and Set writes to both local and Redis.
type SharedSessionCache struct {
	local *SessionCache
	clock stytch.Clock
	ttl   time.Duration
}

// NewSharedSessionCache creates a SharedSessionCache.
// The Redis client is read from configs.RDB; if nil, falls back to local-only.
func NewSharedSessionCache(ttl time.Duration, clock stytch.Clock) *SharedSessionCache {
	return &SharedSessionCache{
		local: NewSessionCache(ttl, clock),
		clock: clock,
		ttl:   ttl,
	}
}

// Get checks the local cache first, then Redis if available.
func (s *SharedSessionCache) Get(ctx context.Context, userID, workspaceID string, currentVersion int64) (SessionContext, bool) {
	// Tier 1: local cache.
	if sc, ok := s.local.Get(userID, workspaceID, currentVersion); ok {
		return sc, true
	}

	// Tier 2: Redis (if available).
	if configs.RDB == nil {
		return SessionContext{}, false
	}

	redisKey := fmt.Sprintf("ws:session:%s:%s", userID, workspaceID)
	data, err := configs.RDB.Get(ctx, redisKey).Bytes()
	if err == redis.Nil {
		return SessionContext{}, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "session cache: redis read failed", "error", err)
		return SessionContext{}, false
	}

	var entry cacheEntry
	if json.Unmarshal(data, &entry) != nil {
		return SessionContext{}, false
	}

	// Check version and expiry.
	if entry.AuthorityVersion != currentVersion {
		return SessionContext{}, false
	}
	if s.clock.Now().After(entry.ExpiresAt) {
		return SessionContext{}, false
	}

	// Populate local cache from Redis hit.
	s.local.Set(entry.Context)

	return entry.Context, true
}

// Set stores in both local cache and Redis (when available).
func (s *SharedSessionCache) Set(ctx context.Context, sc SessionContext) {
	s.local.Set(sc)

	if configs.RDB == nil {
		return
	}

	entry := cacheEntry{
		Context:          sc,
		AuthorityVersion: sc.AuthorityVersion,
		ExpiresAt:        s.clock.Now().Add(s.ttl),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	redisKey := fmt.Sprintf("ws:session:%s:%s", sc.UserID, sc.WorkspaceID)
	if err := configs.RDB.Set(ctx, redisKey, data, s.ttl).Err(); err != nil {
		slog.ErrorContext(ctx, "session cache: redis write failed", "error", err)
	}
}

// Invalidate removes from both local and Redis.
func (s *SharedSessionCache) Invalidate(ctx context.Context, userID, workspaceID string) {
	s.local.Invalidate(userID, workspaceID)

	if configs.RDB == nil {
		return
	}

	redisKey := fmt.Sprintf("ws:session:%s:%s", userID, workspaceID)
	if err := configs.RDB.Del(ctx, redisKey).Err(); err != nil {
		slog.ErrorContext(ctx, "session cache: redis invalidation failed", "error", err)
	}
}
