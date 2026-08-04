package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisFeedCache adapts configs.RDB (or any redis.Cmdable) to the feedCache
// interface consumed by CachedStore. It hides Redis-specific commands and
// error classification from cache-aside logic.
type RedisFeedCache struct {
	commands         redis.Cmdable
	operationTimeout time.Duration
}

// NewRedisFeedCache constructs an adapter around an existing Redis command
// client. operationTimeout bounds every individual Redis command in addition
// to the caller's own context deadline.
func NewRedisFeedCache(commands redis.Cmdable, operationTimeout time.Duration) (*RedisFeedCache, error) {
	if commands == nil {
		return nil, errors.New("notifications: redis feed cache commands client is required")
	}
	if operationTimeout <= 0 {
		return nil, errors.New("notifications: redis feed cache operation timeout must be positive")
	}
	return &RedisFeedCache{commands: commands, operationTimeout: operationTimeout}, nil
}

// errCacheMiss classifies an absent cache entry, distinguishing it from other
// Redis command failures that must fail the request open to MongoDB.
var errCacheMiss = errors.New("notifications: cache miss")

func (c *RedisFeedCache) boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.operationTimeout)
}

// Get returns the raw cached payload for key, or errCacheMiss when absent.
func (c *RedisFeedCache) Get(ctx context.Context, key string) ([]byte, error) {
	opCtx, cancel := c.boundedContext(ctx)
	defer cancel()

	payload, err := c.commands.Get(opCtx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errCacheMiss
		}
		return nil, fmt.Errorf("notifications: cache get: %w", err)
	}
	return payload, nil
}

// Set stores payload under key with a positive TTL. Callers must never pass a
// non-positive ttl; NewCachedStore and the jittered-TTL fill path both
// enforce this before Set is reached.
func (c *RedisFeedCache) Set(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("notifications: cache set requires a positive ttl")
	}

	opCtx, cancel := c.boundedContext(ctx)
	defer cancel()

	if err := c.commands.Set(opCtx, key, payload, ttl).Err(); err != nil {
		return fmt.Errorf("notifications: cache set: %w", err)
	}
	return nil
}

// Delete best-effort removes key. Missing keys are not an error.
func (c *RedisFeedCache) Delete(ctx context.Context, key string) error {
	opCtx, cancel := c.boundedContext(ctx)
	defer cancel()

	if err := c.commands.Del(opCtx, key).Err(); err != nil {
		return fmt.Errorf("notifications: cache delete: %w", err)
	}
	return nil
}

// Generation returns the current per-user generation, treating a missing key
// as generation zero rather than an error.
func (c *RedisFeedCache) Generation(ctx context.Context, key string) (uint64, error) {
	opCtx, cancel := c.boundedContext(ctx)
	defer cancel()

	generation, err := c.commands.Get(opCtx, key).Uint64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("notifications: cache generation read: %w", err)
	}
	return generation, nil
}

// BumpMany increments the generation for every distinct key in one pipeline.
// It is a no-op for an empty key set; generation keys are never given an
// expiry so they persist across TTL-based feed entry eviction.
func (c *RedisFeedCache) BumpMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	opCtx, cancel := c.boundedContext(ctx)
	defer cancel()

	_, err := c.commands.Pipelined(opCtx, func(pipe redis.Pipeliner) error {
		for _, key := range keys {
			pipe.Incr(opCtx, key)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("notifications: cache generation bump: %w", err)
	}
	return nil
}

var _ feedCache = (*RedisFeedCache)(nil)
