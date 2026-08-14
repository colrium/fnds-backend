package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/configs"

	"github.com/alicebob/miniredis/v2"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

func TestRedisCacheBypassesUnavailableRedisAndNonGETRequests(t *testing.T) {
	t.Run("nil Redis client", func(t *testing.T) {
		previous := configs.RDB
		configs.RDB = nil
		t.Cleanup(func() { configs.RDB = previous })

		calls := 0
		recorder, err := runCachedRequest(
			t,
			http.MethodGet,
			"/resource",
			CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
			func(c echo.Context) error {
				calls++
				return c.String(http.StatusOK, "fresh")
			},
		)
		if err != nil {
			t.Fatalf("cached handler error = %v", err)
		}
		if calls != 1 {
			t.Errorf("handler calls = %d, want 1", calls)
		}
		if got := recorder.Header().Get("X-Cache"); got != "" {
			t.Errorf("X-Cache = %q, want empty", got)
		}
	})

	t.Run("non-GET method", func(t *testing.T) {
		server := useTestRedis(t)
		recorder, err := runCachedRequest(
			t,
			http.MethodPost,
			"/resource",
			CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
			func(c echo.Context) error {
				return c.String(http.StatusCreated, "created")
			},
		)
		if err != nil {
			t.Fatalf("cached handler error = %v", err)
		}
		if got := recorder.Header().Get("X-Cache"); got != "" {
			t.Errorf("X-Cache = %q, want empty", got)
		}
		if server.Exists("test:/resource") {
			t.Fatal("non-GET response was cached")
		}
	})
}

func TestRedisCacheMissStoresResponseWithCanonicalQueryKey(t *testing.T) {
	server := useTestRedis(t)
	cfg := CacheConfig{TTL: 3 * time.Minute, KeyPrefix: "artisan-profile"}
	calls := 0
	handler := func(c echo.Context) error {
		calls++
		c.Response().Header().Set("Cache-Control", "public, max-age=60")
		return c.JSON(http.StatusCreated, map[string]string{"source": "handler"})
	}

	first, err := runCachedRequest(
		t,
		http.MethodGet,
		"/api/v1/artisans/jane?b=2&a=1",
		cfg,
		handler,
	)
	if err != nil {
		t.Fatalf("first cached handler error = %v", err)
	}
	if got := first.Header().Get("X-Cache"); got != "MISS" {
		t.Errorf("first X-Cache = %q, want MISS", got)
	}

	key := "artisan-profile:/api/v1/artisans/jane?a=1&b=2"
	payload, err := server.Get(key)
	if err != nil {
		t.Fatalf("stored cache entry %q: %v", key, err)
	}
	cached, ok := decodeCachedResponse([]byte(payload))
	if !ok {
		t.Fatalf("stored cache payload is invalid: %s", payload)
	}
	if cached.Status != http.StatusCreated {
		t.Errorf("cached status = %d, want %d", cached.Status, http.StatusCreated)
	}
	wantContentType := first.Header().Get(echo.HeaderContentType)
	if cached.ContentType != wantContentType {
		t.Errorf("cached content type = %q, want %q", cached.ContentType, wantContentType)
	}
	if cached.CacheControl != "public, max-age=60" {
		t.Errorf("cached Cache-Control = %q", cached.CacheControl)
	}
	if ttl := server.TTL(key); ttl != cfg.TTL {
		t.Errorf("cache TTL = %v, want %v", ttl, cfg.TTL)
	}

	second, err := runCachedRequest(
		t,
		http.MethodGet,
		"/api/v1/artisans/jane?a=1&b=2",
		cfg,
		handler,
	)
	if err != nil {
		t.Fatalf("second cached handler error = %v", err)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
	if got := second.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("second X-Cache = %q, want HIT", got)
	}
}

func TestRedisCacheCoalescesConcurrentMisses(t *testing.T) {
	_ = useTestRedis(t)
	const requestCount = 12

	var calls atomic.Int32
	handler := RedisCache(CacheConfig{
		TTL:       time.Minute,
		KeyPrefix: "artisan-profile",
	})(func(c echo.Context) error {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		return c.String(http.StatusOK, "profile")
	})

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, requestCount)
	errs := make(chan error, requestCount)
	var workers sync.WaitGroup
	for range requestCount {
		workers.Go(func() {
			<-start
			e := echo.New()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/artisans/jane", nil)
			recorder := httptest.NewRecorder()
			errs <- handler(e.NewContext(request, recorder))
			responses <- recorder
		})
	}
	close(start)
	workers.Wait()
	close(errs)
	close(responses)

	for err := range errs {
		if err != nil {
			t.Errorf("cached handler error = %v", err)
		}
	}
	for recorder := range responses {
		if recorder.Code != http.StatusOK || recorder.Body.String() != "profile" {
			t.Errorf("response = (%d, %q), want (200, profile)", recorder.Code, recorder.Body.String())
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("downstream handler calls = %d, want 1", got)
	}
}

func TestRedisCacheHitRestoresResponseMetadata(t *testing.T) {
	_ = useTestRedis(t)
	key := "test:/cached"
	want := cachedResponse{
		Version:      cachedResponseVersion,
		Status:       http.StatusAccepted,
		ContentType:  "application/problem+json",
		CacheControl: "public, s-maxage=300",
		Body:         []byte(`{"message":"cached"}`),
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal cached response: %v", err)
	}
	if err := configs.RDB.Set(t.Context(), key, payload, time.Minute).Err(); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	called := false
	recorder, err := runCachedRequest(
		t,
		http.MethodGet,
		"/cached",
		CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
		func(c echo.Context) error {
			called = true
			return c.NoContent(http.StatusInternalServerError)
		},
	)
	if err != nil {
		t.Fatalf("cached handler error = %v", err)
	}
	if called {
		t.Fatal("downstream handler was called for cache hit")
	}
	if recorder.Code != want.Status {
		t.Errorf("status = %d, want %d", recorder.Code, want.Status)
	}
	if got := recorder.Header().Get(echo.HeaderContentType); got != want.ContentType {
		t.Errorf("Content-Type = %q, want %q", got, want.ContentType)
	}
	if got := recorder.Header().Get("Cache-Control"); got != want.CacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, want.CacheControl)
	}
	if got := recorder.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("X-Cache = %q, want HIT", got)
	}
	if got := recorder.Body.String(); got != string(want.Body) {
		t.Errorf("body = %q, want %q", got, want.Body)
	}
}

func TestRedisCacheDeletesInvalidPayloads(t *testing.T) {
	server := useTestRedis(t)
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "malformed JSON", payload: []byte("legacy body")},
		{name: "wrong version", payload: mustMarshalCachedResponse(t, cachedResponse{Version: 2, Status: 200, ContentType: "text/plain", Body: []byte("body")})},
		{name: "invalid status", payload: mustMarshalCachedResponse(t, cachedResponse{Version: 1, Status: 500, ContentType: "text/plain", Body: []byte("body")})},
		{name: "empty body", payload: mustMarshalCachedResponse(t, cachedResponse{Version: 1, Status: 200, ContentType: "text/plain"})},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := "test:/invalid-" + string(rune('a'+index))
			if err := configs.RDB.Set(t.Context(), key, test.payload, time.Minute).Err(); err != nil {
				t.Fatalf("seed invalid cache entry: %v", err)
			}

			recorder, err := runCachedRequest(
				t,
				http.MethodGet,
				strings.TrimPrefix(key, "test:"),
				CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
				func(c echo.Context) error { return c.NoContent(http.StatusNoContent) },
			)
			if err != nil {
				t.Fatalf("cached handler error = %v", err)
			}
			if got := recorder.Header().Get("X-Cache"); got != "MISS" {
				t.Errorf("X-Cache = %q, want MISS", got)
			}
			if server.Exists(key) {
				t.Errorf("invalid cache entry %q was not deleted", key)
			}
		})
	}
}

func TestRedisCacheDoesNotStoreNonCacheableResponses(t *testing.T) {
	server := useTestRedis(t)
	handlerErr := errors.New("handler failed")
	tests := []struct {
		name    string
		path    string
		handler echo.HandlerFunc
		wantErr error
	}{
		{
			name: "error status with body",
			path: "/server-error",
			handler: func(c echo.Context) error {
				return c.String(http.StatusInternalServerError, "failed")
			},
		},
		{
			name:    "successful empty body",
			path:    "/empty",
			handler: func(c echo.Context) error { return c.NoContent(http.StatusNoContent) },
		},
		{
			name:    "handler error",
			path:    "/handler-error",
			handler: func(echo.Context) error { return handlerErr },
			wantErr: handlerErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runCachedRequest(
				t,
				http.MethodGet,
				test.path,
				CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
				test.handler,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("cached handler error = %v, want %v", err, test.wantErr)
			}
			if server.Exists("test:" + test.path) {
				t.Errorf("non-cacheable response %q was cached", test.path)
			}
		})
	}
}

func TestInvalidateArtisanCacheSupersedesOnlyTargetResource(t *testing.T) {
	server := useTestRedis(t)
	slug := "jane-doe-plumber-nairobi"
	exactKeys := []string{
		"artisan-profile:/api/v1/artisans/" + slug,
		"artisan-share:/artisan/" + slug,
	}
	queryKeys := []string{
		"artisan-profile:/api/v1/artisans/" + slug + "?a=1",
		"artisan-share:/artisan/" + slug + "?ref=share",
	}
	for _, key := range append(exactKeys, queryKeys...) {
		mustSetRedis(t, server, key, "cached")
	}
	unrelated := "artisan-profile:/api/v1/artisans/another-artisan"
	mustSetRedis(t, server, unrelated, "cached")

	if err := InvalidateArtisanCache(context.Background(), slug); err != nil {
		t.Fatalf("InvalidateArtisanCache() error = %v", err)
	}
	for _, key := range exactKeys {
		if server.Exists(key) {
			t.Errorf("cache key %q was not deleted", key)
		}
	}
	for _, key := range queryKeys {
		if !server.Exists(key) {
			t.Errorf("query cache key %q should remain for TTL eviction", key)
		}
	}
	if !server.Exists(unrelated) {
		t.Errorf("unrelated cache key %q was deleted", unrelated)
	}
	for _, generationKey := range []string{
		cacheGenerationKey("artisan-profile", "/api/v1/artisans/"+slug),
		cacheGenerationKey("artisan-share", "/artisan/"+slug),
	} {
		if generation, err := server.Get(generationKey); err != nil || generation != "1" {
			t.Errorf("generation %q = %q, %v; want 1, nil", generationKey, generation, err)
		}
	}
}

func TestInvalidateArtisanCachePreservesOtherArtisanHit(t *testing.T) {
	server := useTestRedis(t)
	const (
		invalidatedSlug = "jane-doe-plumber-nairobi"
		unrelatedSlug   = "john-doe-electrician-nakuru"
	)
	unrelatedKey := "artisan-profile:/api/v1/artisans/" + unrelatedSlug
	payload := mustMarshalCachedResponse(t, cachedResponse{
		Version:     cachedResponseVersion,
		Generation:  0,
		Status:      http.StatusOK,
		ContentType: echo.MIMETextPlainCharsetUTF8,
		Body:        []byte("john cached"),
	})
	mustSetRedis(t, server, unrelatedKey, string(payload))

	if err := InvalidateArtisanCache(context.Background(), invalidatedSlug); err != nil {
		t.Fatalf("InvalidateArtisanCache() error = %v", err)
	}

	called := false
	recorder, err := runCachedRequest(
		t,
		http.MethodGet,
		"/api/v1/artisans/"+unrelatedSlug,
		CacheConfig{TTL: time.Minute, KeyPrefix: "artisan-profile"},
		func(c echo.Context) error {
			called = true
			return c.String(http.StatusOK, "fresh")
		},
	)
	if err != nil {
		t.Fatalf("cached handler error = %v", err)
	}
	if called {
		t.Fatal("unrelated artisan cache entry was superseded")
	}
	if got := recorder.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("X-Cache = %q, want HIT", got)
	}
	if got := recorder.Body.String(); got != "john cached" {
		t.Errorf("body = %q, want john cached", got)
	}
}

func TestInvalidateArtisanCacheErrorContracts(t *testing.T) {
	t.Run("empty slug is a no-op", func(t *testing.T) {
		previous := configs.RDB
		configs.RDB = nil
		t.Cleanup(func() { configs.RDB = previous })

		if err := InvalidateArtisanCache(context.Background(), ""); err != nil {
			t.Errorf("InvalidateArtisanCache(\"\") error = %v, want nil", err)
		}
	})

	t.Run("nil Redis client returns error", func(t *testing.T) {
		previous := configs.RDB
		configs.RDB = nil
		t.Cleanup(func() { configs.RDB = previous })

		err := InvalidateArtisanCache(context.Background(), "artisan")
		if err == nil || err.Error() != "redis client unavailable" {
			t.Errorf("InvalidateArtisanCache() error = %v, want redis client unavailable", err)
		}
	})

	t.Run("Redis command failures are returned with context", func(t *testing.T) {
		server := useTestRedis(t)
		server.SetError("forced Redis failure")

		err := InvalidateArtisanCache(context.Background(), "artisan")
		if err == nil {
			t.Fatal("InvalidateArtisanCache() error = nil, want Redis command error")
		}
		if !strings.Contains(err.Error(), "deleting cache key") {
			t.Errorf("InvalidateArtisanCache() error = %q, want deletion context", err)
		}
	})
}

func TestInvalidateArtisanCacheByPattern(t *testing.T) {
	t.Run("empty pattern is a no-op", func(t *testing.T) {
		previous := configs.RDB
		configs.RDB = nil
		t.Cleanup(func() { configs.RDB = previous })

		if err := InvalidateArtisanCacheByPattern(context.Background(), ""); err != nil {
			t.Errorf("InvalidateArtisanCacheByPattern(\"\") error = %v, want nil", err)
		}
	})

	t.Run("nil Redis client returns error", func(t *testing.T) {
		previous := configs.RDB
		configs.RDB = nil
		t.Cleanup(func() { configs.RDB = previous })

		err := InvalidateArtisanCacheByPattern(context.Background(), "artisan-*")
		if err == nil || err.Error() != "redis client unavailable" {
			t.Errorf("InvalidateArtisanCacheByPattern() error = %v, want redis client unavailable", err)
		}
	})

	t.Run("matching keys are deleted", func(t *testing.T) {
		server := useTestRedis(t)
		mustSetRedis(t, server, "artisan-profile:/one", "cached")
		mustSetRedis(t, server, "artisan-share:/two", "cached")
		mustSetRedis(t, server, "other:/three", "cached")

		if err := InvalidateArtisanCacheByPattern(context.Background(), "artisan-*"); err != nil {
			t.Fatalf("InvalidateArtisanCacheByPattern() error = %v", err)
		}
		if server.Exists("artisan-profile:/one") || server.Exists("artisan-share:/two") {
			t.Fatal("matching artisan cache keys were not deleted")
		}
		if !server.Exists("other:/three") {
			t.Fatal("non-matching cache key was deleted")
		}
	})
}

func useTestRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		MaxRetries:   -1,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	previousClient := configs.RDB
	configs.RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		configs.RDB = previousClient
	})
	return server
}

func runCachedRequest(
	t *testing.T,
	method string,
	target string,
	cfg CacheConfig,
	next echo.HandlerFunc,
) (*httptest.ResponseRecorder, error) {
	t.Helper()

	e := echo.New()
	request := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	return recorder, redisCache(cfg, cacheResponse)(next)(ctx)
}

func mustMarshalCachedResponse(t *testing.T, cached cachedResponse) []byte {
	t.Helper()

	payload, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal cached response: %v", err)
	}
	return payload
}

func mustSetRedis(t *testing.T, server *miniredis.Miniredis, key, value string) {
	t.Helper()
	if err := server.Set(key, value); err != nil {
		t.Fatalf("set Redis key %q: %v", key, err)
	}
}

func TestRedisCacheProductionWrapperBypassesUnavailableRedis(t *testing.T) {
	previous := configs.RDB
	configs.RDB = nil
	t.Cleanup(func() { configs.RDB = previous })

	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/wrapper", nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	calls := 0
	handler := RedisCache(CacheConfig{TTL: time.Minute, KeyPrefix: "test"})(func(c echo.Context) error {
		calls++
		return c.String(http.StatusOK, "fresh")
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("RedisCache() handler error = %v", err)
	}
	if calls != 1 || recorder.Code != http.StatusOK || recorder.Body.String() != "fresh" {
		t.Errorf("result = (calls=%d, status=%d, body=%q), want (1, 200, fresh)", calls, recorder.Code, recorder.Body.String())
	}
}

func TestRedisCacheDetectsMissingContentTypeOnMissAndHit(t *testing.T) {
	t.Run("miss", func(t *testing.T) {
		_ = useTestRedis(t)
		writer := &responseWriterWithoutContentType{header: make(http.Header)}
		e := echo.New()
		request := httptest.NewRequest(http.MethodGet, "/detected-miss", nil)
		ctx := e.NewContext(request, writer)
		var scheduled cachedResponse

		err := redisCache(
			CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
			func(_ string, cached cachedResponse, _ time.Duration) { scheduled = cached },
		)(func(c echo.Context) error {
			c.Response().WriteHeader(http.StatusOK)
			_, err := c.Response().Write([]byte("<html><body>cached</body></html>"))
			return err
		})(ctx)
		if err != nil {
			t.Fatalf("redisCache() error = %v", err)
		}
		if got, want := scheduled.ContentType, "text/html; charset=utf-8"; got != want {
			t.Errorf("scheduled Content-Type = %q, want %q", got, want)
		}
	})

	t.Run("hit", func(t *testing.T) {
		_ = useTestRedis(t)
		payload := mustMarshalCachedResponse(t, cachedResponse{
			Version: cachedResponseVersion,
			Status:  http.StatusOK,
			Body:    []byte("<html><body>cached</body></html>"),
		})
		if err := configs.RDB.Set(t.Context(), "test:/detected-hit", payload, time.Minute).Err(); err != nil {
			t.Fatalf("seed cache: %v", err)
		}

		recorder, err := runCachedRequest(
			t,
			http.MethodGet,
			"/detected-hit",
			CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
			func(echo.Context) error { return errors.New("cache hit called downstream") },
		)
		if err != nil {
			t.Fatalf("redisCache() error = %v", err)
		}
		if got, want := recorder.Header().Get(echo.HeaderContentType), "text/html; charset=utf-8"; got != want {
			t.Errorf("hit Content-Type = %q, want %q", got, want)
		}
	})
}

func TestRedisCacheWriteFailureIsFailOpen(t *testing.T) {
	server := useTestRedis(t)
	configs.RDB.AddHook(failingRedisCommandHook{command: "eval", err: errors.New("forced cache write failure")})

	recorder, err := runCachedRequest(
		t,
		http.MethodGet,
		"/set-failure",
		CacheConfig{TTL: time.Minute, KeyPrefix: "test"},
		func(c echo.Context) error { return c.String(http.StatusOK, "fresh") },
	)
	if err != nil {
		t.Fatalf("redisCache() error = %v, want fail-open response", err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "fresh" {
		t.Errorf("response = (%d, %q), want (200, fresh)", recorder.Code, recorder.Body.String())
	}
	if server.Exists("test:/set-failure") {
		t.Fatal("response was cached despite forced SET failure")
	}
}

func TestInvalidateArtisanCacheByPatternPropagatesCommandFailures(t *testing.T) {
	t.Run("SCAN failure", func(t *testing.T) {
		_ = useTestRedis(t)
		configs.RDB.AddHook(failingRedisCommandHook{command: "scan", err: errors.New("forced SCAN failure")})

		err := InvalidateArtisanCacheByPattern(context.Background(), "artisan-*")
		if err == nil || !strings.Contains(err.Error(), "scanning cache keys") || !strings.Contains(err.Error(), "forced SCAN failure") {
			t.Errorf("error = %v, want contextual SCAN failure", err)
		}
	})

	t.Run("DEL failure", func(t *testing.T) {
		server := useTestRedis(t)
		mustSetRedis(t, server, "artisan-profile:/one", "cached")
		configs.RDB.AddHook(failingRedisCommandHook{command: "del", err: errors.New("forced DEL failure")})

		err := InvalidateArtisanCacheByPattern(context.Background(), "artisan-*")
		if err == nil || !strings.Contains(err.Error(), "deleting 1 cache keys") || !strings.Contains(err.Error(), "forced DEL failure") {
			t.Errorf("error = %v, want contextual DEL failure", err)
		}
		if !server.Exists("artisan-profile:/one") {
			t.Fatal("matching key was deleted despite forced DEL failure")
		}
	})
}

type responseWriterWithoutContentType struct {
	header http.Header
	body   strings.Builder
	status int
}

func (w *responseWriterWithoutContentType) Header() http.Header { return w.header }

func (w *responseWriterWithoutContentType) WriteHeader(status int) { w.status = status }

func (w *responseWriterWithoutContentType) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

type failingRedisCommandHook struct {
	command string
	err     error
}

func (h failingRedisCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h failingRedisCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == h.command {
			return h.err
		}
		return next(ctx, cmd)
	}
}

func (h failingRedisCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRedisCacheProductionWrapperStoresResponse(t *testing.T) {
	server := useTestRedis(t)
	cfg := CacheConfig{TTL: time.Minute, KeyPrefix: "production-wrapper"}
	calls := 0
	handler := RedisCache(cfg)(func(c echo.Context) error {
		calls++
		return c.String(http.StatusOK, "fresh")
	})

	run := func() *httptest.ResponseRecorder {
		t.Helper()
		e := echo.New()
		request := httptest.NewRequest(http.MethodGet, "/resource", nil)
		recorder := httptest.NewRecorder()
		if err := handler(e.NewContext(request, recorder)); err != nil {
			t.Fatalf("RedisCache() handler error = %v", err)
		}
		return recorder
	}

	first := run()
	if got := first.Header().Get("X-Cache"); got != "MISS" {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}

	key := "production-wrapper:/resource"
	deadline := time.Now().Add(time.Second)
	for !server.Exists(key) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.Exists(key) {
		t.Fatalf("production RedisCache wrapper did not store %q", key)
	}

	second := run()
	if got := second.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("second X-Cache = %q, want HIT", got)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
}

func TestRedisCacheRejectsWritePreparedBeforeInvalidation(t *testing.T) {
	server := useTestRedis(t)
	const slug = "jane-doe-plumber-nairobi"
	key := "artisan-profile:/api/v1/artisans/" + slug

	var pending cachedResponse
	var pendingTTL time.Duration
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artisans/"+slug, nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	handler := redisCache(
		CacheConfig{TTL: time.Minute, KeyPrefix: "artisan-profile"},
		func(gotKey string, cached cachedResponse, ttl time.Duration) {
			if gotKey != key {
				t.Errorf("scheduled key = %q, want %q", gotKey, key)
			}
			pending = cached
			pendingTTL = ttl
		},
	)(func(c echo.Context) error {
		return c.String(http.StatusOK, "stale public profile")
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("redisCache() error = %v", err)
	}
	if len(pending.Body) == 0 {
		t.Fatal("cache write was not scheduled")
	}

	if err := InvalidateArtisanCache(context.Background(), slug); err != nil {
		t.Fatalf("InvalidateArtisanCache() error = %v", err)
	}
	cacheResponse(key, pending, pendingTTL)

	if server.Exists(key) {
		t.Fatal("response prepared before invalidation was written after invalidation")
	}
	if generation, err := server.Get(cacheGenerationKey("artisan-profile", "/api/v1/artisans/"+slug)); err != nil || generation != "1" {
		t.Errorf("profile cache generation = %q, %v; want 1, nil", generation, err)
	}
}

func TestRedisCacheDoesNotServeSupersededGeneration(t *testing.T) {
	server := useTestRedis(t)
	key := "artisan-profile:/api/v1/artisans/jane"
	payload := mustMarshalCachedResponse(t, cachedResponse{
		Version:     cachedResponseVersion,
		Generation:  0,
		Status:      http.StatusOK,
		ContentType: echo.MIMETextPlainCharsetUTF8,
		Body:        []byte("stale"),
	})
	mustSetRedis(t, server, key, string(payload))
	mustSetRedis(t, server, cacheGenerationKey("artisan-profile", "/api/v1/artisans/jane"), "1")

	calls := 0
	recorder, err := runCachedRequest(
		t,
		http.MethodGet,
		"/api/v1/artisans/jane",
		CacheConfig{TTL: time.Minute, KeyPrefix: "artisan-profile"},
		func(c echo.Context) error {
			calls++
			return c.String(http.StatusOK, "fresh")
		},
	)
	if err != nil {
		t.Fatalf("redisCache() error = %v", err)
	}
	if calls != 1 || recorder.Body.String() != "fresh" {
		t.Errorf("result = (calls=%d, body=%q), want (1, fresh)", calls, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Cache"); got != "MISS" {
		t.Errorf("X-Cache = %q, want MISS", got)
	}
}
