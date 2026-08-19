package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestGuard_AllowedRequests(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)

	e := echo.New()
	handler := limiter.Guard(LimitSignupPerIP, SubjectIP)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// First 5 requests should be allowed
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup", nil)
		req.Header.Set("X-Real-IP", "10.0.0.1")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/employer/signup")

		err := handler(c)
		if err != nil {
			t.Fatalf("request %d returned error: %v", i+1, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got status %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}
}

func TestGuard_Rejection(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)

	e := echo.New()
	handler := limiter.Guard(LimitSignupPerIP, SubjectIP)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Exhaust the 5 allowed requests
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup", nil)
		req.Header.Set("X-Real-IP", "10.0.0.1")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/employer/signup")
		_ = handler(c)
	}

	// 6th request should be rejected with 429
	req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup", nil)
	req.Header.Set("X-Real-IP", "10.0.0.1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/employer/signup")

	err := handler(c)
	if err != nil {
		t.Fatalf("expected nil error (JSON written to response), got: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	// Check Retry-After header (Req 25.3)
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After header not set on 429 response")
	}
	// Should be a positive integer
	if retryAfter == "0" {
		t.Fatal("Retry-After should be positive")
	}

	// Check JSON body
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}
	if body["success"] != false {
		t.Errorf("body.success = %v, want false", body["success"])
	}
	if body["message"] != "Too many requests" {
		t.Errorf("body.message = %v, want 'Too many requests'", body["message"])
	}
	if body["code"] != "signup_ip_rate_limited" {
		t.Errorf("body.code = %v, want 'signup_ip_rate_limited'", body["code"])
	}
}

func TestGuard_DifferentSubjectsIndependent(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)

	e := echo.New()
	handler := limiter.Guard(LimitSignupPerIP, SubjectIP)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Exhaust limit for IP A
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup", nil)
		req.Header.Set("X-Real-IP", "10.0.0.1")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/employer/signup")
		_ = handler(c)
	}

	// IP B should still be allowed
	req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup", nil)
	req.Header.Set("X-Real-IP", "10.0.0.2")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/employer/signup")

	err := handler(c)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("IP B got status %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestGuard_SubjectFormEmail(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)

	e := echo.New()
	handler := limiter.Guard(LimitSignupPerEmail, SubjectFormEmail)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// LimitSignupPerEmail is 3/10m
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup?email=test@example.com", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/employer/signup")
		err := handler(c)
		if err != nil {
			t.Fatalf("request %d returned error: %v", i+1, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got status %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}

	// 4th should be rejected
	req := httptest.NewRequest(http.MethodPost, "/api/v1/employer/signup?email=test@example.com", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/employer/signup")
	_ = handler(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestGuard_SubjectFromHeader(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)

	e := echo.New()
	handler := limiter.Guard(LimitInviteResend, SubjectFromHeader("X-Workspace-ID"))(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// LimitInviteResend is 3/1h
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/invitation/resend", nil)
		req.Header.Set("X-Workspace-ID", "ws_123")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/api/v1/invitation/resend")
		err := handler(c)
		if err != nil {
			t.Fatalf("request %d returned error: %v", i+1, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got status %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}

	// 4th should be rejected
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invitation/resend", nil)
	req.Header.Set("X-Workspace-ID", "ws_123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/invitation/resend")
	_ = handler(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header not set on rejected request")
	}
}

func TestGuard_RetryAfterIsPositive(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	limiter := NewLimiter(nil, clock)

	e := echo.New()
	handler := limiter.Guard(LimitSignupPerIP, SubjectIP)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Exhaust limit
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/signup", nil)
		req.Header.Set("X-Real-IP", "1.2.3.4")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/signup")
		_ = handler(c)
	}

	// Advance clock to near end of window, but still within it
	clock.Advance(9 * time.Minute)

	req := httptest.NewRequest(http.MethodPost, "/signup", nil)
	req.Header.Set("X-Real-IP", "1.2.3.4")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/signup")
	_ = handler(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After not set")
	}
	if retryAfter == "0" {
		t.Fatal("Retry-After must be positive (at least 1)")
	}
}

func TestSubjectIP(t *testing.T) {
	t.Parallel()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "203.0.113.42")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got := SubjectIP(c)
	if got != "203.0.113.42" {
		t.Errorf("SubjectIP = %q, want %q", got, "203.0.113.42")
	}
}

func TestSubjectFormEmail_QueryParam(t *testing.T) {
	t.Parallel()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/signup?email=user@EXAMPLE.COM", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got := SubjectFormEmail(c)
	if got != "user@EXAMPLE.COM" {
		t.Errorf("SubjectFormEmail = %q, want %q", got, "user@EXAMPLE.COM")
	}
}

func TestSubjectFromHeader_Present(t *testing.T) {
	t.Parallel()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "abc123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	extractor := SubjectFromHeader("X-API-Key")
	got := extractor(c)
	if got != "abc123" {
		t.Errorf("SubjectFromHeader = %q, want %q", got, "abc123")
	}
}

func TestSubjectFromHeader_Missing(t *testing.T) {
	t.Parallel()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	extractor := SubjectFromHeader("X-Missing")
	got := extractor(c)
	if got != "" {
		t.Errorf("SubjectFromHeader for missing header = %q, want empty", got)
	}
}
