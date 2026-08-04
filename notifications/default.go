package notifications

import (
	"errors"
	"sync"

	"backend/configs"
)

// defaultStore is the single shared CachedStore configured by
// ConfigureDefaultStore after MongoDB and Redis startup. It backs both the
// authenticated routes' Handler and NewDefaultService's producers, so
// in-process request coalescing and generation invalidation apply
// consistently across reads and writes.
var (
	defaultStoreMu         sync.Mutex
	defaultStore           *CachedStore
	defaultStoreConfigured bool
)

// ConfigureDefaultStore constructs the process-wide notifications CachedStore
// exactly once, wrapping source (ordinarily NewMongoStore()). Call it after
// configs.ConnectDB and configs.ConnectRedis have run. A second call, or a
// nil result, returns an error instead of panicking so callers can fail
// startup deliberately.
func ConfigureDefaultStore(
	source notificationStore,
	cacheSettings configs.NotificationCacheSettings,
	observer cacheObserver,
) error {
	defaultStoreMu.Lock()
	defer defaultStoreMu.Unlock()

	if defaultStoreConfigured {
		return errors.New("notifications: default store already configured")
	}

	var cache feedCache
	if cacheSettings.Enabled && configs.RDB != nil {
		redisCache, err := NewRedisFeedCache(configs.RDB, cacheSettings.OperationTimeout)
		if err != nil {
			return err
		}
		cache = redisCache
	}

	store, err := NewCachedStore(
		source,
		cache,
		CacheConfig{
			Enabled:          cacheSettings.Enabled && cache != nil,
			TTL:              cacheSettings.TTL,
			OperationTimeout: cacheSettings.OperationTimeout,
			AppEnvironment:   configs.AppEnv(),
		},
		observer,
	)
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("notifications: constructed default store is nil")
	}

	defaultStore = store
	defaultStoreConfigured = true
	return nil
}

// DefaultStore returns the shared CachedStore configured by
// ConfigureDefaultStore. Callers that have not configured it (e.g. tests
// constructing their own explicit stores) should not call this function.
func DefaultStore() (*CachedStore, error) {
	defaultStoreMu.Lock()
	defer defaultStoreMu.Unlock()

	if !defaultStoreConfigured || defaultStore == nil {
		return nil, errors.New("notifications: default store is not configured")
	}
	return defaultStore, nil
}

// resetDefaultStoreForTest clears default-store configuration state so tests
// can exercise ConfigureDefaultStore's single-configuration guard in
// isolation. It is only used from this package's own tests.
func resetDefaultStoreForTest() {
	defaultStoreMu.Lock()
	defer defaultStoreMu.Unlock()
	defaultStore = nil
	defaultStoreConfigured = false
}
