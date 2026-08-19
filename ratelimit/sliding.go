package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// SlidingWindowLimiter implements a sliding-window rate limiter using Redis
// sorted sets. Unlike the fixed-window counter, this prevents the 2x burst
// at window boundaries at ~3x the Redis cost per check (Deferred Item 5).
//
// Algorithm: Uses a Redis sorted set where each member is a unique request ID
// (timestamp-based) and the score is the request timestamp. On each Allow call:
// 1. Remove all entries older than the window
// 2. Count remaining entries
// 3. If below max, add the new entry
// 4. Set TTL to the window duration
//
// Falls back to the fixed-window in-memory counter when Redis is unavailable.
type SlidingWindowLimiter struct {
	rdb   *redis.Client
	clock Clock
	// fallback to fixed-window when Redis is nil
	fallback *Limiter
}

// NewSlidingWindowLimiter creates a SlidingWindowLimiter.
// If rdb is nil, all calls fall back to the fixed-window in-memory counter.
func NewSlidingWindowLimiter(rdb *redis.Client, clock Clock) *SlidingWindowLimiter {
	if clock == nil {
		clock = systemClock{}
	}
	return &SlidingWindowLimiter{
		rdb:      rdb,
		clock:    clock,
		fallback: NewLimiter(nil, clock), // in-memory fallback
	}
}

// Allow checks whether the operation is allowed under the sliding window.
// Uses a sorted set in Redis for precise per-rolling-window counting.
//
// Cost: 4 Redis commands per call (ZREMRANGEBYSCORE, ZCARD, ZADD, EXPIRE)
// compared to 2 for the fixed-window (INCR, EXPIRE).
func (s *SlidingWindowLimiter) Allow(ctx context.Context, n Name, subject string) Decision {
	lim, ok := limits[n]
	if !ok {
		return Decision{Allowed: true, Limit: lim}
	}

	// Fall back to fixed-window when Redis is not available.
	if s.rdb == nil {
		return s.fallback.Allow(ctx, n, subject)
	}

	now := s.clock.Now()
	windowStart := now.Add(-lim.Window)
	key := fmt.Sprintf("rl:sw:%s:%s", string(n), subject)

	// Use a pipeline for atomicity.
	pipe := s.rdb.Pipeline()

	// 1. Remove entries older than the window.
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%d", windowStart.UnixMicro()))

	// 2. Count remaining entries.
	countCmd := pipe.ZCard(ctx, key)

	// Execute the read operations. A pipeline failure (e.g. Redis outage) must
	// not silently grant every request a free pass, so deny rather than fall
	// through with a zero count.
	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "sliding window rate limiter: pipeline exec failed", "error", err)
		return Decision{Allowed: false, Limit: lim, RetryAfter: time.Second}
	}

	count := countCmd.Val()

	if count >= int64(lim.Max) {
		// Denied. Calculate retry-after as the time until the oldest entry
		// in the window expires.
		oldestCmd := s.rdb.ZRangeWithScores(ctx, key, 0, 0)
		if err := oldestCmd.Err(); err != nil {
			slog.WarnContext(ctx, "sliding window rate limiter: failed to fetch oldest entry for retry-after", "error", err)
		}
		oldest := oldestCmd.Val()
		retryAfter := lim.Window
		if len(oldest) > 0 {
			oldestTime := time.UnixMicro(int64(oldest[0].Score))
			retryAfter = oldestTime.Add(lim.Window).Sub(now)
			if retryAfter < 0 {
				retryAfter = time.Second
			}
		}
		return Decision{
			Allowed:    false,
			RetryAfter: retryAfter,
			Limit:      lim,
		}
	}

	// 3. Add the new entry with the current timestamp as the score. The member
	// includes a random suffix because two requests can land in the same
	// microsecond under load; an identical member would overwrite the first
	// entry in the sorted set instead of adding a second one.
	member := fmt.Sprintf("%d-%d", now.UnixMicro(), rand.Int63())
	if err := s.rdb.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMicro()), Member: member}).Err(); err != nil {
		slog.ErrorContext(ctx, "sliding window rate limiter: failed to record request", "error", err)
		return Decision{Allowed: false, Limit: lim, RetryAfter: time.Second}
	}

	// 4. Set TTL to the window duration (ensures cleanup if no more requests).
	if err := s.rdb.Expire(ctx, key, lim.Window+time.Second).Err(); err != nil {
		slog.ErrorContext(ctx, "sliding window rate limiter: failed to set TTL on rate limit key", "error", err)
	}

	return Decision{Allowed: true, Limit: lim}
}

// Guard returns an Echo middleware that uses the sliding-window limiter.
// Same interface as the fixed-window Guard for drop-in replacement.
func (s *SlidingWindowLimiter) Guard(n Name, subject func(echo.Context) string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			subj := subject(c)
			decision := s.Allow(c.Request().Context(), n, subj)
			if !decision.Allowed {
				retryAfter := int(decision.RetryAfter.Seconds())
				if retryAfter <= 0 {
					retryAfter = 1
				}
				c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				slog.Warn("sliding window rate limit exceeded",
					"endpoint", c.Path(),
					"limit", string(n),
					"ip", c.RealIP(),
					"subject", subj,
				)
				return c.JSON(http.StatusTooManyRequests, map[string]any{
					"success": false,
					"message": "Too many requests",
					"code":    string(n) + "_rate_limited",
				})
			}
			return next(c)
		}
	}
}
