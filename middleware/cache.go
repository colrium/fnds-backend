package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"backend/configs"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const (
	cachedResponseVersion      = 1
	cacheGenerationPrefix      = "cache-generation:"
	cacheTTLJitterPercent      = 10
	cacheWriteOperationTimeout = 250 * time.Millisecond
)

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
	Resource     string `json:"-"`
	Status       int    `json:"status"`
	ContentType  string `json:"content_type"`
	CacheControl string `json:"cache_control,omitempty"`
	Body         []byte `json:"body"`
}

type cacheResponseScheduler func(string, cachedResponse, time.Duration)

type cacheLoadResult struct {
	owner     *echo.Response
	response  cachedResponse
	shareable bool
	fromCache bool
}

// RedisCache returns an Echo middleware that caches successful responses in
// Redis keyed by the request path and canonical query string. Cache misses fall
// through to the handler. When Redis is unavailable (configs.RDB == nil), the
// middleware is a no-op.
func RedisCache(cfg CacheConfig) echo.MiddlewareFunc {
	return redisCache(cfg, func(key string, cached cachedResponse, ttl time.Duration) {
		cacheResponse(key, cached, jitteredCacheTTL(ttl))
	})
}

func redisCache(cfg CacheConfig, schedule cacheResponseScheduler) echo.MiddlewareFunc {
	var flights singleflight.Group

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
			resource := c.Request().URL.Path
			generation, err := loadCacheGeneration(ctx, cfg.KeyPrefix, resource)
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
			value, err, _ := flights.Do(key, func() (any, error) {
				// A previous flight may have filled Redis after the initial miss.
				currentGeneration, generationErr := loadCacheGeneration(ctx, cfg.KeyPrefix, resource)
				if generationErr == nil {
					if currentPayload, getErr := configs.RDB.Get(ctx, key).Bytes(); getErr == nil {
						if cached, ok := decodeCachedResponse(currentPayload); ok &&
							cached.Generation == currentGeneration {
							return cacheLoadResult{
								response:  cached,
								shareable: true,
								fromCache: true,
							}, nil
						}
					}
					generation = currentGeneration
				}

				body := new(bytes.Buffer)
				origWriter := c.Response().Writer
				c.Response().Writer = &captureWriter{
					ResponseWriter: origWriter,
					buf:            body,
				}
				defer func() {
					c.Response().Writer = origWriter
				}()

				if nextErr := next(c); nextErr != nil {
					return cacheLoadResult{owner: c.Response()}, nextErr
				}

				responseBody := bytes.Clone(body.Bytes())
				contentType := c.Response().Header().Get(echo.HeaderContentType)
				if contentType == "" && len(responseBody) > 0 {
					contentType = http.DetectContentType(responseBody)
				}
				response := cachedResponse{
					Version:      cachedResponseVersion,
					Generation:   generation,
					KeyPrefix:    cfg.KeyPrefix,
					Resource:     resource,
					Status:       c.Response().Status,
					ContentType:  contentType,
					CacheControl: c.Response().Header().Get("Cache-Control"),
					Body:         responseBody,
				}
				cacheable := response.Status >= http.StatusOK &&
					response.Status < http.StatusMultipleChoices &&
					len(response.Body) > 0
				if cacheable {
					schedule(key, response, cfg.TTL)
				}

				return cacheLoadResult{
					owner:     c.Response(),
					response:  response,
					shareable: cacheable,
				}, nil
			})
			if err != nil {
				return err
			}

			result, ok := value.(cacheLoadResult)
			if !ok {
				return errors.New("cache load returned an unexpected result")
			}
			if result.owner == c.Response() {
				return nil
			}
			if !result.shareable {
				return next(c)
			}
			if result.fromCache {
				c.Response().Header().Set("X-Cache", "HIT")
			}
			if result.response.CacheControl != "" {
				c.Response().Header().Set("Cache-Control", result.response.CacheControl)
			}
			return c.Blob(
				result.response.Status,
				result.response.ContentType,
				result.response.Body,
			)
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

func cacheGenerationKey(prefix, resource string) string {
	return cacheGenerationPrefix + prefix + ":" + resource
}

func loadCacheGeneration(ctx context.Context, prefix, resource string) (int64, error) {
	generation, err := configs.RDB.Get(ctx, cacheGenerationKey(prefix, resource)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return generation, err
}

func jitteredCacheTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	span := ttl * cacheTTLJitterPercent / 100
	low := ttl - span
	high := ttl + span
	return low + time.Duration(rand.Int64N(int64(high-low)+1))
}

func cacheResponse(key string, cached cachedResponse, ttl time.Duration) {
	if configs.RDB == nil ||
		len(cached.Body) == 0 ||
		cached.KeyPrefix == "" ||
		cached.Resource == "" {
		return
	}
	payload, err := json.Marshal(cached)
	if err != nil {
		slog.Error("failed to encode cache response", "cache_key", key, "error", err)
		return
	}

	// Atomically reject writes prepared before an invalidation advanced this
	// cache namespace's generation, preventing stale responses from reappearing.
	cacheCtx, cancel := context.WithTimeout(context.Background(), cacheWriteOperationTimeout)
	defer cancel()
	_, err = cacheResponseScript.Run(
		cacheCtx,
		configs.RDB,
		[]string{cacheGenerationKey(cached.KeyPrefix, cached.Resource), key},
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
		prefix   string
		resource string
		baseKey  string
	}{
		{
			prefix:   "artisan-profile",
			resource: "/api/v1/artisans/" + slug,
			baseKey:  "artisan-profile:/api/v1/artisans/" + slug,
		},
		{
			prefix:   "artisan-share",
			resource: "/artisan/" + slug,
			baseKey:  "artisan-share:/artisan/" + slug,
		},
	}
	var invalidationErrors []error
	for _, target := range targets {
		_, err := configs.RDB.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Incr(ctx, cacheGenerationKey(target.prefix, target.resource))
			pipe.Del(ctx, target.baseKey)
			return nil
		})
		if err != nil {
			invalidationErrors = append(
				invalidationErrors,
				fmt.Errorf("advancing generation and deleting cache key %q: %w", target.baseKey, err),
			)
		}
	}
	return errors.Join(invalidationErrors...)
}

// InvalidateArtisanSitemapCache advances the sitemap generation. Old entries
// become unreachable immediately and remain only until their TTL expires.
func InvalidateArtisanSitemapCache(ctx context.Context) error {
	if configs.RDB == nil {
		return errors.New("redis client unavailable")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	const (
		prefix   = "artisan-sitemap"
		resource = "/sitemap-artisans.xml"
		baseKey  = "artisan-sitemap:/sitemap-artisans.xml"
	)
	_, err := configs.RDB.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Incr(ctx, cacheGenerationKey(prefix, resource))
		pipe.Del(ctx, baseKey)
		return nil
	})
	if err != nil {
		return fmt.Errorf("invalidating artisan sitemap cache: %w", err)
	}
	return nil
}

// InvalidateArtisanCacheByPattern removes all cached artisan responses matching
// a pattern. Prefer resource-generation invalidation for normal request paths.
func InvalidateArtisanCacheByPattern(ctx context.Context, pattern string) error {
	if pattern == "" {
		return nil
	}
	if configs.RDB == nil {
		return errors.New("redis client unavailable")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return invalidateCachePattern(ctx, pattern)
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
