package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	feedCacheSchemaVersion    = 1
	feedCacheKeyLayoutVersion = 1
	feedCacheTTLJitterPercent = 10
	feedCacheKeyRoot          = "fundis"
	feedCacheKeyDomain        = "notifications:feed"
)

type feedReader interface {
	List(context.Context, string, int64) (Feed, error)
}

type feedMutator interface {
	MarkRead(context.Context, string, string) (bool, error)
	MarkAllRead(context.Context, string) error
}

type notificationCreator interface {
	Create(context.Context, Notification) error
	CreateMany(context.Context, []Notification) error
}

type notificationStore interface {
	feedReader
	feedMutator
	notificationCreator
}

type cacheValues interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

type cacheGenerations interface {
	Generation(context.Context, string) (uint64, error)
	BumpMany(context.Context, []string) error
}

type feedCache interface {
	cacheValues
	cacheGenerations
}

type cacheObserver interface {
	RecordLookup(string, time.Duration)
	RecordSourceLoad(time.Duration, string)
	RecordWrite(string)
	RecordInvalidation(string, int)
	RecordCoalescedWait()
}

// CacheConfig controls the cache decorator. Environment parsing and Redis
// client construction remain outside the notifications package.
type CacheConfig struct {
	Enabled          bool
	TTL              time.Duration
	OperationTimeout time.Duration
	// AppEnvironment namespaces cache keys (e.g. "production", "staging") so
	// shared Redis infrastructure cannot leak entries across environments.
	AppEnvironment string
}

// CachedStore decorates the authoritative notification store with a
// cache-aside read path and generation-based invalidation. source is always
// the authority for notification data and authorization scope; cache
// failures never change source-of-truth behavior.
type CachedStore struct {
	source   notificationStore
	cache    feedCache
	config   CacheConfig
	observer cacheObserver
	flights  singleflight.Group
}

// NewCachedStore constructs a notification store decorator without performing
// any implicit package initialization or external I/O.
func NewCachedStore(
	source notificationStore,
	cache feedCache,
	config CacheConfig,
	observer cacheObserver,
) (*CachedStore, error) {
	if source == nil {
		return nil, errors.New("notifications: cached store source is required")
	}
	if config.Enabled && config.TTL <= 0 {
		return nil, errors.New("notifications: cache ttl must be positive when enabled")
	}
	if config.Enabled && config.OperationTimeout <= 0 {
		return nil, errors.New("notifications: cache operation timeout must be positive when enabled")
	}
	if config.Enabled && config.AppEnvironment == "" {
		return nil, errors.New("notifications: cache app environment is required when enabled")
	}
	if observer == nil {
		observer = noopCacheObserver{}
	}

	return &CachedStore{
		source:   source,
		cache:    cache,
		config:   config,
		observer: observer,
	}, nil
}

// bypassed reports whether the decorator must skip Redis entirely: disabled
// by configuration, or Redis was never constructed (nil client/adapter).
func (s *CachedStore) bypassed() bool {
	return !s.config.Enabled || s.cache == nil
}

func (s *CachedStore) List(ctx context.Context, userID string, limit int64) (Feed, error) {
	if s.bypassed() {
		s.observer.RecordLookup("bypass", 0)
		return s.source.List(ctx, userID, limit)
	}

	lookupStart := time.Now()
	genKey, err := generationKey(s.config.AppEnvironment, userID)
	if err != nil {
		s.observer.RecordLookup("error", time.Since(lookupStart))
		return s.source.List(ctx, userID, limit)
	}

	generation, err := s.cache.Generation(ctx, genKey)
	if err != nil {
		s.logCacheError("generation_read", err)
		s.observer.RecordLookup("error", time.Since(lookupStart))
		return s.source.List(ctx, userID, limit)
	}

	key, err := feedKey(s.config.AppEnvironment, userID, generation, limit)
	if err != nil {
		s.observer.RecordLookup("error", time.Since(lookupStart))
		return s.source.List(ctx, userID, limit)
	}

	if feed, hit, fallback := s.tryReadFeed(ctx, key); fallback {
		s.observer.RecordLookup("error", time.Since(lookupStart))
		return s.source.List(ctx, userID, limit)
	} else if hit {
		s.observer.RecordLookup("hit", time.Since(lookupStart))
		return feed, nil
	}

	s.observer.RecordLookup("miss", time.Since(lookupStart))
	return s.loadAndFill(ctx, key, genKey, generation, userID, limit)
}

// tryReadFeed attempts a cache read for key. hit is true only for a valid,
// current-version entry. fallback is true when the caller must skip
// remaining cache work for this request and query the source directly
// (a real Redis error, as opposed to an ordinary miss).
func (s *CachedStore) tryReadFeed(ctx context.Context, key string) (feed Feed, hit bool, fallback bool) {
	payload, err := s.cache.Get(ctx, key)
	if err != nil {
		if errors.Is(err, errCacheMiss) {
			return Feed{}, false, false
		}
		s.logCacheError("feed_read", err)
		return Feed{}, false, true
	}

	decoded, classification := decodeFeedCacheEnvelope(payload)
	if classification != feedCacheDecodeValid {
		if delErr := s.cache.Delete(ctx, key); delErr != nil {
			s.logCacheError("invalid_entry_delete", delErr)
		}
		return Feed{}, false, false
	}
	return decoded, true, false
}

type feedLoadResult struct {
	feed Feed
	err  error
}

// loadAndFill coalesces concurrent identical-key misses behind one
// singleflight call, double-checks Redis inside the flight, and fills the
// cache only if the generation has not advanced since the read that started
// this call.
func (s *CachedStore) loadAndFill(
	ctx context.Context,
	key string,
	genKey string,
	generation uint64,
	userID string,
	limit int64,
) (Feed, error) {
	value, _, shared := s.flights.Do(key, func() (any, error) {
		if feed, hit, fallback := s.tryReadFeed(ctx, key); hit {
			return feedLoadResult{feed: feed}, nil
		} else if fallback {
			feed, err := s.source.List(ctx, userID, limit)
			return feedLoadResult{feed: feed, err: err}, nil
		}

		loadStart := time.Now()
		feed, err := s.source.List(ctx, userID, limit)
		if err != nil {
			s.observer.RecordSourceLoad(time.Since(loadStart), "error")
			return feedLoadResult{feed: Feed{}, err: err}, nil
		}
		s.observer.RecordSourceLoad(time.Since(loadStart), "success")

		currentGeneration, genErr := s.cache.Generation(ctx, genKey)
		if genErr != nil {
			s.logCacheError("generation_recheck", genErr)
			return feedLoadResult{feed: feed}, nil
		}
		if currentGeneration != generation {
			// A mutation advanced the generation while we were loading; writing
			// now would poison the active generation with stale data.
			return feedLoadResult{feed: feed}, nil
		}

		s.fillBestEffort(ctx, key, feed)
		return feedLoadResult{feed: feed}, nil
	})

	if shared {
		s.observer.RecordCoalescedWait()
	}

	result, ok := value.(feedLoadResult)
	if !ok {
		// singleflight.Do only returns a non-nil error for panics/Forget races
		// we do not use; this branch should be unreachable in practice.
		return Feed{}, errors.New("notifications: unexpected cache flight result")
	}
	return result.feed, result.err
}

func (s *CachedStore) fillBestEffort(ctx context.Context, key string, feed Feed) {
	payload, err := encodeFeedCacheEnvelope(feed)
	if err != nil {
		s.logCacheError("envelope_encode", err)
		s.observer.RecordWrite("error")
		return
	}

	if err := s.cache.Set(ctx, key, payload, jitteredTTL(s.config.TTL)); err != nil {
		s.logCacheError("feed_write", err)
		s.observer.RecordWrite("error")
		return
	}
	s.observer.RecordWrite("success")
}

// jitteredTTL returns a duration within [90%, 110%] of ttl (inclusive),
// spreading expiry across instances to avoid synchronized cache stampedes.
func jitteredTTL(ttl time.Duration) time.Duration {
	span := ttl * feedCacheTTLJitterPercent / 100
	low := ttl - span
	high := ttl + span
	return low + time.Duration(rand.Int64N(int64(high-low)+1))
}

func (s *CachedStore) Create(ctx context.Context, notification Notification) error {
	if err := s.source.Create(ctx, notification); err != nil {
		return err
	}
	s.invalidateUsers(ctx, []string{notification.UserID})
	return nil
}

func (s *CachedStore) CreateMany(ctx context.Context, notifications []Notification) error {
	affected := distinctUserIDs(notifications)
	err := s.source.CreateMany(ctx, notifications)
	// A bulk insert can partially succeed before reporting an error, so
	// invalidate the intended recipients even when CreateMany fails.
	s.invalidateUsers(ctx, affected)
	return err
}

func (s *CachedStore) MarkRead(
	ctx context.Context,
	userID string,
	notificationID string,
) (bool, error) {
	updated, err := s.source.MarkRead(ctx, userID, notificationID)
	if err != nil || !updated {
		return updated, err
	}
	s.invalidateUsers(ctx, []string{userID})
	return true, nil
}

func (s *CachedStore) MarkAllRead(ctx context.Context, userID string) error {
	if err := s.source.MarkAllRead(ctx, userID); err != nil {
		return err
	}
	s.invalidateUsers(ctx, []string{userID})
	return nil
}

// invalidateUsers bumps the cache generation for every distinct non-empty
// user ID. Failures are recorded and logged but never alter the mutation
// result the caller already committed to the source of truth.
func (s *CachedStore) invalidateUsers(ctx context.Context, userIDs []string) {
	if s.bypassed() {
		return
	}

	seen := make(map[string]struct{}, len(userIDs))
	keys := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}

		key, err := generationKey(s.config.AppEnvironment, userID)
		if err != nil {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return
	}

	if err := s.cache.BumpMany(ctx, keys); err != nil {
		s.logCacheError("invalidation", err)
		s.observer.RecordInvalidation("error", len(keys))
		return
	}
	s.observer.RecordInvalidation("success", len(keys))
}

func distinctUserIDs(notifications []Notification) []string {
	seen := make(map[string]struct{}, len(notifications))
	ids := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		if notification.UserID == "" {
			continue
		}
		if _, exists := seen[notification.UserID]; exists {
			continue
		}
		seen[notification.UserID] = struct{}{}
		ids = append(ids, notification.UserID)
	}
	return ids
}

// logCacheError logs a runtime cache failure once at this boundary. Handlers
// and callers above CachedStore must not log the same failure again.
//
// The error text itself is never logged: Redis command errors can echo back
// the command's arguments (keys, values), and our own errCacheMiss wrapping
// only adds static context, so nothing is lost by logging solely the
// operation name and error type — a bounded, sensitive-data-free classifier.
func (s *CachedStore) logCacheError(operation string, err error) {
	slog.Warn("notifications cache operation failed", "operation", operation, "error_type", fmt.Sprintf("%T", err))
}

type noopCacheObserver struct{}

func (noopCacheObserver) RecordLookup(string, time.Duration)     {}
func (noopCacheObserver) RecordSourceLoad(time.Duration, string) {}
func (noopCacheObserver) RecordWrite(string)                     {}
func (noopCacheObserver) RecordInvalidation(string, int)         {}
func (noopCacheObserver) RecordCoalescedWait()                   {}

var (
	_ notificationStore = (*MongoStore)(nil)
	_ notificationStore = (*CachedStore)(nil)
	_ cacheObserver     = noopCacheObserver{}
)
