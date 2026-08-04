package configs

import (
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestConnectRedisDisabled(t *testing.T) {
	server := miniredis.RunT(t)
	previous := RDB
	existing := redis.NewClient(&redis.Options{Addr: server.Addr()})
	RDB = existing
	t.Cleanup(func() {
		_ = existing.Close()
		if RDB != nil && RDB != existing {
			_ = RDB.Close()
		}
		RDB = previous
	})
	t.Setenv("REDIS_URL", "")

	if err := ConnectRedis(); err != nil {
		t.Fatalf("ConnectRedis() error = %v, want nil", err)
	}
	if RDB != nil {
		t.Fatal("ConnectRedis() left RDB configured when REDIS_URL is empty")
	}
}

func TestConnectRedisInvalidURL(t *testing.T) {
	previous := RDB
	RDB = nil
	t.Cleanup(func() {
		if RDB != nil {
			_ = RDB.Close()
		}
		RDB = previous
	})
	t.Setenv("REDIS_URL", "://invalid")

	err := ConnectRedis()
	if err == nil {
		t.Fatal("ConnectRedis() error = nil, want invalid URL error")
	}
	if !strings.Contains(err.Error(), "parsing REDIS_URL") {
		t.Errorf("ConnectRedis() error = %q, want parsing context", err)
	}
	if RDB != nil {
		t.Fatal("ConnectRedis() configured RDB after URL parsing failed")
	}
}

func TestConnectRedisSuccess(t *testing.T) {
	server := miniredis.RunT(t)
	previous := RDB
	RDB = nil
	t.Cleanup(func() {
		if RDB != nil {
			_ = RDB.Close()
		}
		RDB = previous
	})
	t.Setenv("REDIS_URL", "redis://"+server.Addr())

	if err := ConnectRedis(); err != nil {
		t.Fatalf("ConnectRedis() error = %v, want nil", err)
	}
	if RDB == nil {
		t.Fatal("ConnectRedis() did not configure RDB")
	}
	if err := RDB.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("configured Redis client Ping() error = %v", err)
	}
}

func TestConnectRedisConnectionFailure(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetError("forced Redis failure")

	previous := RDB
	RDB = nil
	t.Cleanup(func() {
		if RDB != nil {
			_ = RDB.Close()
		}
		RDB = previous
	})
	t.Setenv("REDIS_URL", "redis://"+server.Addr())

	err := ConnectRedis()
	if err == nil {
		t.Fatal("ConnectRedis() error = nil, want connection error")
	}
	if !strings.Contains(err.Error(), "connecting to Redis") {
		t.Errorf("ConnectRedis() error = %q, want connection context", err)
	}
	if RDB != nil {
		t.Fatal("ConnectRedis() configured RDB after Ping failed")
	}
}
