package stytch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestHTTPClient(t *testing.T, handler http.HandlerFunc) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := Config{
		ProjectID: "project-test-123",
		Secret:    "secret-test-abc",
		BaseURL:   srv.URL,
	}
	c, err := NewHTTPClient(cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}
	return c
}

func TestDiscoveryEmailSendPostsToStytch(t *testing.T) {
	var gotPath string
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200})
	})

	if err := client.DiscoveryEmailSend(context.Background(), DiscoveryEmailSendInput{
		Email:       "test@example.com",
		RedirectURL: "https://fundis.example/onboarding",
	}); err != nil {
		t.Fatalf("DiscoveryEmailSend() error = %v", err)
	}
	if gotPath != "/v1/b2b/magic_links/email/discovery/send" {
		t.Errorf("path = %q, want /v1/b2b/magic_links/email/discovery/send", gotPath)
	}
}

func TestCreateOrganizationFromDiscoveryParsesResponse(t *testing.T) {
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status_code": 200,
			"member_id":   "member-123",
			"session_jwt": "jwt-abc",
			"organization": map[string]any{
				"organization_id": "org-456",
			},
		})
	})

	result, err := client.CreateOrganizationFromDiscovery(context.Background(), CreateOrgInput{
		IntermediateSessionToken: "ist-1",
		Name:                     "Acme",
	})
	if err != nil {
		t.Fatalf("CreateOrganizationFromDiscovery() error = %v", err)
	}
	if result.OrganizationID != "org-456" || result.MemberID != "member-123" {
		t.Errorf("result = %+v, want organization_id=org-456 member_id=member-123", result)
	}
}

func TestClassifyMapsNotFoundStatus(t *testing.T) {
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status_code":   404,
			"error_type":    "organization_not_found",
			"error_message": "organization not found",
		})
	})

	_, err := client.GetOrganization(context.Background(), "missing-org")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true; err = %v", err)
	}
}

func TestClassifyMapsRateLimitedStatus(t *testing.T) {
	client := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status_code":   429,
			"error_type":    "rate_limited",
			"error_message": "too many requests",
		})
	})

	err := client.DeleteOrganization(context.Background(), "org-1")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !IsUnavailable(err) {
		t.Errorf("IsUnavailable(err) = false, want true (rate-limited maps to unavailable at the edge); err = %v", err)
	}
}

func TestClassifyMapsUnexpectedNetworkErrorToUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed server: every request fails at the transport level.

	cfg := Config{ProjectID: "p", Secret: "s", BaseURL: srv.URL}
	client, err := NewHTTPClient(cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient() error = %v", err)
	}

	callErr := client.DeleteOrganization(context.Background(), "org-1")
	if callErr == nil {
		t.Fatal("expected an error, got nil")
	}
	var stytchErr *Error
	if !errors.As(callErr, &stytchErr) || stytchErr.Kind != KindUnavailable {
		t.Errorf("expected Kind=KindUnavailable, got %v", callErr)
	}
}
