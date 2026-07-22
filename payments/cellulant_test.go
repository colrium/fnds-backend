package payments

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCellulantEncryptMatchesLegacyPHP(t *testing.T) {
	service := NewCellulantService(CellulantConfig{
		SecretKey: "test-secret",
		IVKey:     "test-iv",
	})
	payload := []byte(
		`{"merchant_transaction_id":"subscription:test","request_amount":100,"currency_code":"KES"}`,
	)

	got, err := service.Encrypt(payload)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	const want = "ZGZINjlGdTc0V2YxaWdYNVhYald6YWc0R2wvRnp2b0RMRmJPa1ZpaVlTRmlqWUY0Y2syb1JwWFZpWlBBdzdkYS9IelBJWVZ4UzFEWnJaWWlOQkJPZU1BL3lVWVUrTmhRWkUweUpBV3FqdUFmbmpSdGtrTVp2aDdQQjRyMHZmSUU="
	if got != want {
		t.Fatalf("Encrypt() = %q, want legacy PHP output %q", got, want)
	}
}

func TestCellulantCheckoutURL(t *testing.T) {
	service := NewCellulantService(CellulantConfig{
		AccessKey:   "access-key",
		SecretKey:   "test-secret",
		IVKey:       "test-iv",
		CheckoutURL: "https://online.uat.tingg.africa/testing/express/checkout",
	})
	checkoutURL, err := service.CheckoutURL(CellulantCheckoutRequest{
		MerchantTransactionID: "CEL-SUB-123",
		RequestAmount:         100,
		CurrencyCode:          "KES",
	})
	if err != nil {
		t.Fatalf("CheckoutURL() error = %v", err)
	}
	parsed, err := url.Parse(checkoutURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if got := parsed.Query().Get("access_key"); got != "access-key" {
		t.Fatalf("access_key = %q, want access-key", got)
	}
	if parsed.Query().Get("encrypted_payload") == "" {
		t.Fatal("encrypted_payload is empty")
	}
}

func TestStatusFromCellulantCode(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{178, "successful"},
		{188, "successful"},
		{130, "pending"},
		{177, "pending"},
		{183, "pending"},
		{129, "expired"},
		{180, "failed"},
	}
	for _, tt := range tests {
		if got := StatusFromCellulantCode(tt.code); got != tt.want {
			t.Errorf("StatusFromCellulantCode(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestCellulantNumericFieldsAcceptStrings(t *testing.T) {
	if got := firstInt("188", float64(0)); got != 188 {
		t.Fatalf("firstInt() = %d, want 188", got)
	}
	if got := firstFloat("350.00", float64(0)); got != 350 {
		t.Fatalf("firstFloat() = %f, want 350", got)
	}
	if got := firstInt(188.5); got != 0 {
		t.Fatalf("firstInt() accepted fractional status as %d", got)
	}
}

func TestParseCellulantEventSupportsLegacySnakeCase(t *testing.T) {
	event := parseCellulantEvent(map[string]interface{}{
		"results": map[string]interface{}{
			"checkout_request_id":     "checkout-1",
			"merchant_transaction_id": "CEL-SUB-123",
			"account_number":          "CEL-SUB-123",
			"request_status_code":     "188",
			"amount_paid":             "350.00",
			"request_currency_code":   "KES",
			"service_code":            "FUNDIS",
		},
	})
	if event.RequestStatusCode != 188 || event.AmountPaid != 350 {
		t.Fatalf("parseCellulantEvent() = %#v", event)
	}
}

func TestVerifyCellulantPaymentRequeriesProvider(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"access_token":"token"}`
		if strings.Contains(request.URL.Path, "/checkout-api/query/") {
			if request.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
			}
			body = `{"results":{"merchant_transaction_id":"CEL-SUB-123","request_status_code":"188"}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	service := NewCellulantService(CellulantConfig{
		BaseURL:      "https://cellulant.example",
		ClientID:     "client",
		ClientSecret: "secret",
		APIKey:       "api-key",
		ServiceCode:  "FUNDIS",
		HTTPClient:   client,
	})
	payload, err := service.VerifyPayment(context.Background(), "CEL-SUB-123")
	if err != nil {
		t.Fatalf("VerifyPayment() error = %v", err)
	}
	event := parseCellulantEvent(payload)
	if event.MerchantTransactionID != "CEL-SUB-123" || event.RequestStatusCode != 188 {
		t.Fatalf("verified event = %#v", event)
	}
}
