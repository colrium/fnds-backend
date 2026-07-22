package payments

import (
	"net/url"
	"testing"
)

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
