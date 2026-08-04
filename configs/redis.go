package configs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// RDB is the shared Redis client, initialised at startup by ConnectRedis.
// Nil when REDIS_URL is not configured (caching degrades gracefully).
var RDB *redis.Client

// ConnectRedis parses REDIS_URL and verifies the connection. An empty URL
// intentionally disables caching; configured but invalid or unreachable Redis
// returns an error so the application can fail fast.
func ConnectRedis() error {
	url := GetRedisUrl()
	if url == "" {
		RDB = nil
		slog.Warn("REDIS_URL not set — response caching disabled")
		return nil
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		return fmt.Errorf("parsing REDIS_URL: %w", err)
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		pingErr := fmt.Errorf("connecting to Redis: %w", err)
		if closeErr := client.Close(); closeErr != nil {
			return errors.Join(pingErr, fmt.Errorf("closing Redis client: %w", closeErr))
		}
		return pingErr
	}

	RDB = client
	slog.Info("connected to Redis")
	return nil
}
