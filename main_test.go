package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/middleware"

	"github.com/labstack/echo/v4"
)

func TestNewPublicArtisanLimiterEnforcesBurstAndUsesIndependentStores(t *testing.T) {
	newApp := func(limiter echo.MiddlewareFunc) *echo.Echo {
		app := echo.New()
		app.IPExtractor = echo.ExtractIPDirect()
		app.GET("/", func(c echo.Context) error {
			return c.NoContent(http.StatusNoContent)
		}, limiter)
		return app
	}
	request := func(app *echo.Echo) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.10:4321"
		response := httptest.NewRecorder()
		app.ServeHTTP(response, req)
		return response.Code
	}

	first := newApp(newPublicArtisanLimiter())
	second := newApp(newPublicArtisanLimiter())
	for i := 0; i < 30; i++ {
		if status := request(first); status != http.StatusNoContent {
			t.Fatalf("first limiter request %d status = %d, want 204", i+1, status)
		}
	}
	throttled := false
	for i := 0; i < 10; i++ {
		if status := request(first); status == http.StatusTooManyRequests {
			throttled = true
			break
		} else if status != http.StatusNoContent {
			t.Fatalf("first limiter post-burst request %d status = %d, want 204 or 429", i+1, status)
		}
	}
	if !throttled {
		t.Error("first limiter did not throttle after exhausting its burst")
	}
	if status := request(second); status != http.StatusNoContent {
		t.Errorf("independent limiter first request status = %d, want 204", status)
	}
}

func TestRegisterPublicArtisanRoutes(t *testing.T) {
	t.Parallel()

	app := echo.New()
	api := app.Group("/api/v1")

	limiterCount := 0
	newLimiter := func() echo.MiddlewareFunc {
		limiterCount++
		limiterID := fmt.Sprintf("limiter-%d", limiterCount)
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				c.Response().Header().Set("X-Test-Limiter", limiterID)
				return next(c)
			}
		}
	}

	cacheConfigs := []middleware.CacheConfig{}
	newCache := func(cfg middleware.CacheConfig) echo.MiddlewareFunc {
		cacheConfigs = append(cacheConfigs, cfg)
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				c.Response().Header().Set("X-Test-Cache", cfg.KeyPrefix)
				return next(c)
			}
		}
	}

	registerPublicArtisanRoutes(app, api, publicArtisanRouteDependencies{
		registerAPIRoutes: func(group *echo.Group) {
			group.GET("/artisans/:slug", func(c echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"route": "api"})
			})
		},
		shareHandler: func(c echo.Context) error {
			return c.HTML(http.StatusOK, "<html><body>share</body></html>")
		},
		sitemapHandler: func(c echo.Context) error {
			return c.Blob(
				http.StatusOK,
				"application/xml; charset=UTF-8",
				[]byte("<?xml version=\"1.0\"?><urlset>sitemap</urlset>"),
			)
		},
		newLimiter: newLimiter,
		newCache:   newCache,
	})

	if limiterCount != 2 {
		t.Fatalf("limiter factory calls = %d, want 2 independent crawler and API limiters", limiterCount)
	}

	expectedTTLs := map[string]time.Duration{
		"artisan-share":   5 * time.Minute,
		"artisan-sitemap": 10 * time.Minute,
		"artisan-profile": 5 * time.Minute,
	}
	if len(cacheConfigs) != len(expectedTTLs) {
		t.Fatalf("cache configurations = %d, want %d", len(cacheConfigs), len(expectedTTLs))
	}
	for _, cfg := range cacheConfigs {
		expectedTTL, ok := expectedTTLs[cfg.KeyPrefix]
		if !ok {
			t.Errorf("unexpected cache prefix %q", cfg.KeyPrefix)
			continue
		}
		if cfg.TTL != expectedTTL {
			t.Errorf("cache TTL for %q = %s, want %s", cfg.KeyPrefix, cfg.TTL, expectedTTL)
		}
		delete(expectedTTLs, cfg.KeyPrefix)
	}
	for prefix := range expectedTTLs {
		t.Errorf("missing cache configuration for prefix %q", prefix)
	}

	routes := map[string]bool{}
	for _, route := range app.Routes() {
		if route.Method == http.MethodGet {
			routes[route.Path] = true
		}
	}
	for _, path := range []string{
		"/api/v1/artisans/:slug",
		"/artisan/:slug",
		"/sitemap-artisans.xml",
	} {
		if !routes[path] {
			t.Errorf("GET route %q was not registered", path)
		}
	}

	tests := []struct {
		name                string
		path                string
		expectedBody        string
		expectedContentType string
		expectedCache       string
		expectedLimiter     string
	}{
		{
			name:                "JSON artisan API uses API limiter",
			path:                "/api/v1/artisans/jane-doe",
			expectedBody:        `"route":"api"`,
			expectedContentType: echo.MIMEApplicationJSON,
			expectedCache:       "artisan-profile",
			expectedLimiter:     "limiter-2",
		},
		{
			name:                "HTML artisan page uses crawler limiter",
			path:                "/artisan/jane-doe",
			expectedBody:        "share",
			expectedContentType: echo.MIMETextHTML,
			expectedCache:       "artisan-share",
			expectedLimiter:     "limiter-1",
		},
		{
			name:                "XML sitemap shares crawler limiter",
			path:                "/sitemap-artisans.xml",
			expectedBody:        "sitemap",
			expectedContentType: echo.MIMEApplicationXML,
			expectedCache:       "artisan-sitemap",
			expectedLimiter:     "limiter-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			response := httptest.NewRecorder()

			app.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Body.String(), tt.expectedBody) {
				t.Errorf("body = %q, want it to contain %q", response.Body.String(), tt.expectedBody)
			}
			if contentType := response.Header().Get(echo.HeaderContentType); !strings.HasPrefix(contentType, tt.expectedContentType) {
				t.Errorf("Content-Type = %q, want prefix %q", contentType, tt.expectedContentType)
			}
			if cache := response.Header().Get("X-Test-Cache"); cache != tt.expectedCache {
				t.Errorf("cache middleware = %q, want %q", cache, tt.expectedCache)
			}
			if limiter := response.Header().Get("X-Test-Limiter"); limiter != tt.expectedLimiter {
				t.Errorf("limiter middleware = %q, want %q", limiter, tt.expectedLimiter)
			}
		})
	}
}
