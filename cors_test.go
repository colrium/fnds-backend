package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestIsFundisOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "apex", origin: "https://fundis.co.ke", want: true},
		{name: "staging frontend", origin: "https://staging.fundis.co.ke", want: true},
		{name: "nested subdomain", origin: "https://preview.staging.fundis.co.ke", want: true},
		{name: "backend subdomain", origin: "https://staging-api.fundis.co.ke", want: true},
		{name: "insecure scheme", origin: "http://staging.fundis.co.ke", want: false},
		{name: "lookalike suffix", origin: "https://fundis.co.ke.evil.example", want: false},
		{name: "lookalike prefix", origin: "https://evilfundis.co.ke", want: false},
		{name: "malformed", origin: "not a URL", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isFundisOrigin(tt.origin); got != tt.want {
				t.Fatalf("isFundisOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		appEnv     string
		origin     string
		wantOrigin string
	}{
		{
			name:       "Fundis subdomain",
			appEnv:     "staging",
			origin:     "https://staging.fundis.co.ke",
			wantOrigin: "https://staging.fundis.co.ke",
		},
		{
			name:       "localhost in development",
			appEnv:     "development",
			origin:     "http://localhost:5173",
			wantOrigin: "http://localhost:5173",
		},
		{
			name:   "localhost outside development",
			appEnv: "staging",
			origin: "http://localhost:5173",
		},
		{
			name:   "unrelated origin",
			appEnv: "staging",
			origin: "https://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			app := echo.New()
			app.Use(newCORSMiddleware(tt.appEnv))
			app.GET("/", func(ctx echo.Context) error {
				return ctx.NoContent(http.StatusNoContent)
			})

			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(echo.HeaderOrigin, tt.origin)
			recorder := httptest.NewRecorder()
			app.ServeHTTP(recorder, request)

			if got := recorder.Header().Get(echo.HeaderAccessControlAllowOrigin); got != tt.wantOrigin {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantOrigin)
			}
		})
	}
}

func TestCORSPreflightAllowsAuthorization(t *testing.T) {
	app := echo.New()
	app.Use(newCORSMiddleware("staging"))
	app.GET("/", func(ctx echo.Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	request.Header.Set(echo.HeaderOrigin, "https://staging.fundis.co.ke")
	request.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodGet)
	request.Header.Set(echo.HeaderAccessControlRequestHeaders, echo.HeaderAuthorization)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if got := recorder.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "https://staging.fundis.co.ke" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := recorder.Header().Get(echo.HeaderAccessControlAllowHeaders); !strings.Contains(got, echo.HeaderAuthorization) {
		t.Fatalf("Access-Control-Allow-Headers = %q, want it to include %q", got, echo.HeaderAuthorization)
	}
}
