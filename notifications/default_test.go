package notifications

import (
	"testing"
	"time"

	"backend/configs"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestConfigureDefaultStoreOnceThenErrorOnSecondCall(t *testing.T) {
	resetDefaultStoreForTest()
	t.Cleanup(resetDefaultStoreForTest)

	previousRDB := configs.RDB
	configs.RDB = nil
	t.Cleanup(func() { configs.RDB = previousRDB })

	settings := configs.NotificationCacheSettings{Enabled: false}
	if err := ConfigureDefaultStore(&fakeNotificationStore{}, settings, nil); err != nil {
		t.Fatalf("ConfigureDefaultStore() first call error = %v", err)
	}

	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("DefaultStore() = nil, want configured store")
	}

	if err := ConfigureDefaultStore(&fakeNotificationStore{}, settings, nil); err == nil {
		t.Fatal("ConfigureDefaultStore() second call error = nil, want error on double configuration")
	}
}

func TestDefaultStoreErrorsBeforeConfiguration(t *testing.T) {
	resetDefaultStoreForTest()
	t.Cleanup(resetDefaultStoreForTest)

	if _, err := DefaultStore(); err == nil {
		t.Fatal("DefaultStore() error = nil, want error before ConfigureDefaultStore")
	}
}

func TestNewDefaultServicePanicsWithoutConfiguration(t *testing.T) {
	resetDefaultStoreForTest()
	t.Cleanup(resetDefaultStoreForTest)

	defer func() {
		if recover() == nil {
			t.Fatal("NewDefaultService() did not panic without a configured default store")
		}
	}()
	NewDefaultService()
}

func TestConfigureDefaultStoreWithRedisConfigured(t *testing.T) {
	resetDefaultStoreForTest()
	t.Cleanup(resetDefaultStoreForTest)
	t.Setenv("APP_ENV", "test")

	previousRDB := configs.RDB
	t.Cleanup(func() { configs.RDB = previousRDB })
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	configs.RDB = client

	settings := configs.NotificationCacheSettings{
		Enabled:          true,
		TTL:              30 * time.Second,
		OperationTimeout: 100 * time.Millisecond,
	}
	if err := ConfigureDefaultStore(&fakeNotificationStore{}, settings, nil); err != nil {
		t.Fatalf("ConfigureDefaultStore() error = %v", err)
	}
	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore() error = %v", err)
	}
	if store.bypassed() {
		t.Fatal("DefaultStore() bypassed = true, want cache enabled when Redis is configured")
	}
}
