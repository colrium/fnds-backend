package turnstile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestVerifySkipsWhenSecretEmpty(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	called := false
	withSiteVerifyServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	if err := Verify(context.Background(), "any-token", "1.2.3.4", "login"); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if called {
		t.Fatal("siteverify was called without a configured secret")
	}
}

func TestVerifyFailsClosedInProductionWithoutSecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	err := Verify(context.Background(), "any-token", "1.2.3.4", "login")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Verify() error = %v, want ErrUnavailable", err)
	}
}

func TestVerifyRejectsInvalidTokenBeforeRequest(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	called := false
	withSiteVerifyServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "whitespace", token: "  "},
		{name: "too long", token: strings.Repeat("x", maxTokenLength+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(context.Background(), tt.token, "1.2.3.4", "login")
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Verify() error = %v, want ErrInvalid", err)
			}
		})
	}
	if called {
		t.Fatal("siteverify was called for an invalid token")
	}
}

func TestVerifySendsExpectedRequestAndAcceptsValidResponse(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	t.Setenv("TURNSTILE_HOSTNAME", "app.example.com")

	withSiteVerifyServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		want := url.Values{
			"secret":   {"secret"},
			"response": {"good-token"},
			"remoteip": {"1.2.3.4"},
		}
		if got := r.Form; got.Encode() != want.Encode() {
			t.Errorf("form = %v, want %v", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"success":true,"hostname":"APP.EXAMPLE.COM","action":"login"}`,
		))
	}))

	if err := Verify(context.Background(), "good-token", "1.2.3.4", "login"); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestVerifyClassifiesSiteverifyFailures(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")

	tests := []struct {
		name       string
		response   string
		wantTarget error
	}{
		{
			name:       "invalid token",
			response:   `{"success":false,"error-codes":["invalid-input-response"]}`,
			wantTarget: ErrInvalid,
		},
		{
			name:       "duplicate token",
			response:   `{"success":false,"error-codes":["timeout-or-duplicate"]}`,
			wantTarget: ErrInvalid,
		},
		{
			name:       "cloudflare internal error",
			response:   `{"success":false,"error-codes":["internal-error"]}`,
			wantTarget: ErrUnavailable,
		},
		{
			name:       "invalid secret",
			response:   `{"success":false,"error-codes":["invalid-input-secret"]}`,
			wantTarget: ErrUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSiteVerifyServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.response))
			}))

			err := Verify(context.Background(), "token", "", "login")
			if !errors.Is(err, tt.wantTarget) {
				t.Fatalf("Verify() error = %v, want %v", err, tt.wantTarget)
			}
		})
	}
}

func TestVerifyRejectsMismatchedResponseMetadata(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")

	tests := []struct {
		name             string
		expectedAction   string
		expectedHostname string
		response         string
	}{
		{
			name:           "action",
			expectedAction: "register",
			response:       `{"success":true,"hostname":"app.example.com","action":"login"}`,
		},
		{
			name:             "hostname",
			expectedAction:   "login",
			expectedHostname: "app.example.com",
			response:         `{"success":true,"hostname":"attacker.example","action":"login"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TURNSTILE_HOSTNAME", tt.expectedHostname)
			withSiteVerifyServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.response))
			}))

			err := Verify(context.Background(), "token", "", tt.expectedAction)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Verify() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestVerifyTreatsBadHTTPResponsesAsUnavailable(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")

	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "non-200", statusCode: http.StatusBadGateway, body: `{}`},
		{name: "malformed JSON", statusCode: http.StatusOK, body: `{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSiteVerifyServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))

			err := Verify(context.Background(), "token", "", "login")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Verify() error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestVerifyHonorsContextCancellation(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	withSiteVerifyServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"action":"login"}`))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Verify(ctx, "token", "", "login")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Verify() error = %v, want ErrUnavailable", err)
	}
}

func withSiteVerifyServer(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	oldURL := SiteVerifyURL
	SiteVerifyURL = server.URL
	t.Cleanup(func() {
		SiteVerifyURL = oldURL
	})
}
