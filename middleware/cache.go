package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backend/configs"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

const cachedResponseVersion = 1

const cacheGenerationPrefix = "cache-generation:"

// CacheConfig configures the Redis response cache middleware.
type CacheConfig struct {
	// TTL is how long a cached response lives in Redis.
	TTL time.Duration
	// KeyPrefix distinguishes different cached route groups.
	KeyPrefix string
}

type cachedResponse struct {
	Version      int    `json:"version"`
	Generation   int64  `json:"generation"`
	KeyPrefix    string `json:"-"`
	Status       int    `json:"status"`
	ContentType  string `json:"content_type"`
	CacheControl string `json:"cache_control,omitempty"`
	Body         []byte `json:"body"`
}

type cacheResponseScheduler func(string, cachedResponse, time.Duration)

// RedisCache returns an Echo middleware that caches successful responses in
// Redis keyed by the request path and canonical query string. Cache misses fall
// through to the handler. When Redis is unavailable (configs.RDB == nil), the
// middleware is a no-op.
func RedisCache(cfg CacheConfig) echo.MiddlewareFunc {
	return redisCache(cfg, func(key string, cached cachedResponse, ttl time.Duration) {
		go cacheResponse(key, cached, ttl)
	})
}

func redisCache(cfg CacheConfig, schedule cacheResponseScheduler) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Skip caching when Redis is not configured.
			if configs.RDB == nil {
				return next(c)
			}

			// Only cache GET requests.
			if c.Request().Method != http.MethodGet {
				return next(c)
			}

			ctx := c.Request().Context()
			generation, err := loadCacheGeneration(ctx, cfg.KeyPrefix)
			if err != nil {
				slog.Error("failed to load cache generation", "cache_prefix", cfg.KeyPrefix, "error", err)
				return next(c)
			}

			key := cfg.KeyPrefix + ":" + c.Request().URL.Path
			if query := c.Request().URL.Query().Encode(); query != "" {
				key += "?" + query
			}

			// Try to serve from cache.
			payload, err := configs.RDB.Get(ctx, key).Bytes()
			if err == nil {
				if cached, ok := decodeCachedResponse(payload); ok && cached.Generation == generation {
					c.Response().Header().Set("X-Cache", "HIT")
					if cached.CacheControl != "" {
						c.Response().Header().Set("Cache-Control", cached.CacheControl)
					}
					return c.Blob(cached.Status, cached.ContentType, cached.Body)
				}

				// Invalid, legacy, or superseded entries cannot be served safely.
				if err := configs.RDB.Del(ctx, key).Err(); err != nil {
					slog.Error("failed to remove invalid cache entry", "cache_key", key, "error", err)
				}
			}

			// Cache miss — capture the response.
			c.Response().Header().Set("X-Cache", "MISS")
			body := new(bytes.Buffer)
			origWriter := c.Response().Writer
			c.Response().Writer = &captureWriter{
				ResponseWriter: origWriter,
				buf:            body,
			}

			if err := next(c); err != nil {
				return err
			}

			// Only cache successful responses (2xx).
			if c.Response().Status >= 200 && c.Response().Status < 300 && body.Len() > 0 {
				responseBody := bytes.Clone(body.Bytes())
				contentType := c.Response().Header().Get(echo.HeaderContentType)
				if contentType == "" {
					contentType = http.DetectContentType(responseBody)
				}
				schedule(key, cachedResponse{
					Version:      cachedResponseVersion,
					Generation:   generation,
					KeyPrefix:    cfg.KeyPrefix,
					Status:       c.Response().Status,
					ContentType:  contentType,
					CacheControl: c.Response().Header().Get("Cache-Control"),
					Body:         responseBody,
				}, cfg.TTL)
			}

			return nil
		}
	}
}

func decodeCachedResponse(payload []byte) (cachedResponse, bool) {
	var cached cachedResponse
	if err := json.Unmarshal(payload, &cached); err != nil {
		return cachedResponse{}, false
	}
	if cached.Version != cachedResponseVersion ||
		cached.Status < http.StatusOK || cached.Status >= http.StatusMultipleChoices ||
		len(cached.Body) == 0 {
		return cachedResponse{}, false
	}
	if cached.ContentType == "" {
		cached.ContentType = http.DetectContentType(cached.Body)
	}
	return cached, true
}

// captureWriter tees writes to both the original ResponseWriter and a buffer.
type captureWriter struct {
	http.ResponseWriter
	buf *bytes.Buffer
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

var cacheResponseScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then
  current = "0"
end
if current ~= ARGV[1] then
  return 0
end
if tonumber(ARGV[3]) > 0 then
  redis.call("SET", KEYS[2], ARGV[2], "PX", ARGV[3])
else
  redis.call("SET", KEYS[2], ARGV[2])
end
return 1
`)

func cacheGenerationKey(prefix string) string {
	return cacheGenerationPrefix + prefix
}

func loadCacheGeneration(ctx context.Context, prefix string) (int64, error) {
	generation, err := configs.RDB.Get(ctx, cacheGenerationKey(prefix)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return generation, err
}

func cacheResponse(key string, cached cachedResponse, ttl time.Duration) {
	if configs.RDB == nil || len(cached.Body) == 0 || cached.KeyPrefix == "" {
		return
	}
	payload, err := json.Marshal(cached)
	if err != nil {
		slog.Error("failed to encode cache response", "cache_key", key, "error", err)
		return
	}

	// Atomically reject writes prepared before an invalidation advanced this
	// cache namespace's generation, preventing stale responses from reappearing.
	cacheCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = cacheResponseScript.Run(
		cacheCtx,
		configs.RDB,
		[]string{cacheGenerationKey(cached.KeyPrefix), key},
		strconv.FormatInt(cached.Generation, 10),
		payload,
		ttl.Milliseconds(),
	).Result()
	if err != nil {
		slog.Error("failed to cache response", "cache_key", key, "error", err)
	}
}

// InvalidateArtisanCache removes the cached public profile for a given slug.
// Call after a profile update or toggle so the next request sees fresh data.
func InvalidateArtisanCache(ctx context.Context, slug string) error {
	if slug == "" {
		return nil
	}
	if configs.RDB == nil {
		return errors.New("redis client unavailable")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	targets := []struct {
		prefix  string
		baseKey string
	}{
		{prefix: "artisan-profile", baseKey: "artisan-profile:/api/v1/artisans/" + slug},
		{prefix: "artisan-share", baseKey: "artisan-share:/artisan/" + slug},
	}
	var invalidationErrors []error
	for _, target := range targets {
		_, err := configs.RDB.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Incr(ctx, cacheGenerationKey(target.prefix))
			pipe.Del(ctx, target.baseKey)
			return nil
		})
		if err != nil {
			invalidationErrors = append(
				invalidationErrors,
				fmt.Errorf("advancing generation and deleting cache key %q: %w", target.baseKey, err),
			)
		}
		pattern := target.baseKey + "\\?*"
		if err := invalidateCachePattern(ctx, pattern); err != nil {
			invalidationErrors = append(
				invalidationErrors,
				fmt.Errorf("invalidating cache pattern %q: %w", pattern, err),
			)
		}
	}
	return errors.Join(invalidationErrors...)
}

// InvalidateArtisanCacheByPattern removes all cached artisan responses matching
// a pattern. Useful for sitemap invalidation.
func InvalidateArtisanCacheByPattern(ctx context.Context, pattern string) error {
	if pattern == "" {
		return nil
	}
	if configs.RDB == nil {
		return errors.New("redis client unavailable")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if prefix, ok := exactCachePrefix(pattern); ok {
		if err := configs.RDB.Incr(ctx, cacheGenerationKey(prefix)).Err(); err != nil {
			return fmt.Errorf("advancing cache generation for %q: %w", prefix, err)
		}
	}
	return invalidateCachePattern(ctx, pattern)
}

func exactCachePrefix(pattern string) (string, bool) {
	separator := strings.IndexByte(pattern, ':')
	if separator <= 0 {
		return "", false
	}
	prefix := pattern[:separator]
	if strings.ContainsAny(prefix, `*?[]\\`) {
		return "", false
	}
	return prefix, true
}

func invalidateCachePattern(ctx context.Context, pattern string) error {
	iter := configs.RDB.Scan(ctx, 0, pattern, 100).Iterator()
	keys := []string{}
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scanning cache keys: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	if err := configs.RDB.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("deleting %d cache keys: %w", len(keys), err)
	}
	return nil
}

var _ redis.Cmdable = (*redis.Client)(nil)
