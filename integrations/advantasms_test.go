package integrations

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func configureAdvanta(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("ADVANTA_API_KEY", "test-key")
	t.Setenv("ADVANTA_PARTNER_ID", "123")
	t.Setenv("ADVANTA_SHORTCODE", "FUNDIS")
	t.Setenv("ADVANTA_BASE_URL", baseURL)
}

func TestSendAdvantaSMSSuccess(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"responses":[{"response-code":200,"response-description":"Success","mobile":"254700000000","messageid":123}]}`)
	}))
	defer srv.Close()

	configureAdvanta(t, srv.URL)

	if err := SendAdvantaSMS("254700000000", "hello"); err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if gotPath != advantaSendPath {
		t.Errorf("path = %q, want %q", gotPath, advantaSendPath)
	}
	for _, key := range []string{"apikey", "partnerID", "message", "shortcode", "mobile"} {
		if _, ok := gotBody[key]; !ok {
			t.Errorf("request body missing field %q; got %v", key, gotBody)
		}
	}
	if gotBody["message"] != "hello" || gotBody["mobile"] != "254700000000" {
		t.Errorf("unexpected body values: %v", gotBody)
	}
}

func TestSendAdvantaSMSProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"responses":[{"response-code":1006,"response-description":"Invalid credentials","mobile":"254700000000"}]}`)
	}))
	defer srv.Close()

	configureAdvanta(t, srv.URL)

	err := SendAdvantaSMS("254700000000", "hello")
	if err == nil {
		t.Fatal("expected error for non-200 response-code, got nil")
	}
	if !strings.Contains(err.Error(), "1006") {
		t.Errorf("error should mention provider code 1006, got: %v", err)
	}
}

func TestSendAdvantaSMSUnconfigured(t *testing.T) {
	t.Setenv("ADVANTA_API_KEY", "")
	t.Setenv("ADVANTA_PARTNER_ID", "")
	t.Setenv("ADVANTA_SHORTCODE", "")
	if err := SendAdvantaSMS("254700000000", "hello"); err == nil {
		t.Error("expected error when credentials are missing, got nil")
	}
}
