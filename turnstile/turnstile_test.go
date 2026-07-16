package turnstile

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestVerifySkipsWhenSecretEmpty(t *testing.T) {
	os.Unsetenv("TURNSTILE_SECRET_KEY")
	// Point at a server that would fail the test if called.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("siteverify should not be called when secret is empty")
	}))
	defer srv.Close()
	old := SiteVerifyURL
	SiteVerifyURL = srv.URL
	defer func() { SiteVerifyURL = old }()

	if err := Verify("any-token", "1.2.3.4"); err != nil {
		t.Fatalf("Verify() = %v, want nil (skip)", err)
	}
}

func TestVerifyErrorsOnEmptyTokenWhenConfigured(t *testing.T) {
	os.Setenv("TURNSTILE_SECRET_KEY", "secret")
	defer os.Unsetenv("TURNSTILE_SECRET_KEY")

	if err := Verify("", "1.2.3.4"); err == nil {
		t.Fatal("Verify() = nil, want error for empty token")
	}
}

func TestVerifySuccess(t *testing.T) {
	os.Setenv("TURNSTILE_SECRET_KEY", "secret")
	defer os.Unsetenv("TURNSTILE_SECRET_KEY")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	old := SiteVerifyURL
	SiteVerifyURL = srv.URL
	defer func() { SiteVerifyURL = old }()

	if err := Verify("good-token", "1.2.3.4"); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestVerifyFailure(t *testing.T) {
	os.Setenv("TURNSTILE_SECRET_KEY", "secret")
	defer os.Unsetenv("TURNSTILE_SECRET_KEY")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	defer srv.Close()
	old := SiteVerifyURL
	SiteVerifyURL = srv.URL
	defer func() { SiteVerifyURL = old }()

	if err := Verify("bad-token", "1.2.3.4"); err == nil {
		t.Fatal("Verify() = nil, want error for failed verification")
	}
}

func TestVerifyErrorsOnNon200(t *testing.T) {
	os.Setenv("TURNSTILE_SECRET_KEY", "secret")
	defer os.Unsetenv("TURNSTILE_SECRET_KEY")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := SiteVerifyURL
	SiteVerifyURL = srv.URL
	defer func() { SiteVerifyURL = old }()

	if err := Verify("any-token", "1.2.3.4"); err == nil {
		t.Fatal("Verify() = nil, want error for non-200 status")
	}
}
