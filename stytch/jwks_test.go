package stytch

import (
	"crypto/rsa"
	"errors"
	"testing"
	"time"
)

func TestVerifySessionJWT_ValidToken(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)
	cfg := Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}

	adapter := NewWithClient(cfg, fake)

	// Mint a valid JWT using the Fake's signing key.
	memberID := "member-abc123"
	orgID := "org-xyz789"
	tokenStr := fake.MintSessionJWT(memberID, orgID, time.Hour)

	// Verify it.
	session, err := adapter.VerifySessionJWT(tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if session.MemberID != memberID {
		t.Errorf("MemberID = %q, want %q", session.MemberID, memberID)
	}
	if session.OrganizationID != orgID {
		t.Errorf("OrganizationID = %q, want %q", session.OrganizationID, orgID)
	}
	if session.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}
	expectedExpiry := clock.Now().Add(time.Hour)
	if session.ExpiresAt.Unix() != expectedExpiry.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", session.ExpiresAt, expectedExpiry)
	}
}

func TestVerifySessionJWT_ExpiredToken(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)
	cfg := Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}

	adapter := NewWithClient(cfg, fake)

	// Mint a token with a short TTL.
	tokenStr := fake.MintSessionJWT("member-1", "org-1", 30*time.Minute)

	// Advance the clock past expiry.
	clock.Advance(31 * time.Minute)

	// Verification should fail.
	_, err := adapter.VerifySessionJWT(tokenStr)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}

	var stytchErr *Error
	if !errors.As(err, &stytchErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if stytchErr.Kind != KindUnauthenticated {
		t.Errorf("Kind = %v, want KindUnauthenticated", stytchErr.Kind)
	}
}

func TestVerifySessionJWT_InvalidSignature(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)
	cfg := Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}

	adapter := NewWithClient(cfg, fake)

	// Mint a valid token with one fake.
	tokenStr := fake.MintSessionJWT("member-1", "org-1", time.Hour)

	// Tamper with the token (modify a character in the signature).
	tampered := tokenStr[:len(tokenStr)-5] + "XXXXX"

	_, err := adapter.VerifySessionJWT(tampered)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}

	var stytchErr *Error
	if !errors.As(err, &stytchErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if stytchErr.Kind != KindUnauthenticated {
		t.Errorf("Kind = %v, want KindUnauthenticated", stytchErr.Kind)
	}
}

func TestVerifySessionJWT_WrongIssuer(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)

	// Use a different ProjectID than the one in the JWT (which uses "project-test").
	cfg := Config{
		ProjectID:     "project-wrong",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}

	adapter := NewWithClient(cfg, fake)

	// Mint a valid token (issuer will be "stytch.com/project-test").
	tokenStr := fake.MintSessionJWT("member-1", "org-1", time.Hour)

	_, err := adapter.VerifySessionJWT(tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong issuer, got nil")
	}

	var stytchErr *Error
	if !errors.As(err, &stytchErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if stytchErr.Kind != KindUnauthenticated {
		t.Errorf("Kind = %v, want KindUnauthenticated", stytchErr.Kind)
	}
}

func TestVerifySessionJWT_GarbageToken(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)
	cfg := Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}

	adapter := NewWithClient(cfg, fake)

	_, err := adapter.VerifySessionJWT("not-a-valid-jwt")
	if err == nil {
		t.Fatal("expected error for garbage token, got nil")
	}

	var stytchErr *Error
	if !errors.As(err, &stytchErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if stytchErr.Kind != KindUnauthenticated {
		t.Errorf("Kind = %v, want KindUnauthenticated", stytchErr.Kind)
	}
}

func TestVerifySessionJWT_NilJWKSCache(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	cfg := Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}

	// Create adapter without a JWKSSource client (nil client).
	adapter := NewWithClient(cfg, nil)

	_, err := adapter.VerifySessionJWT("some.jwt.token")
	if err == nil {
		t.Fatal("expected error when JWKS cache is nil, got nil")
	}

	var stytchErr *Error
	if !errors.As(err, &stytchErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if stytchErr.Kind != KindUnauthenticated {
		t.Errorf("Kind = %v, want KindUnauthenticated", stytchErr.Kind)
	}
}

func TestJWKSCache_StaleServeOnRefreshFailure(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)

	fetchCalls := 0
	failFetch := false

	cache := newJWKSCache(time.Hour, clock, func() ([]byte, error) {
		fetchCalls++
		if failFetch {
			return nil, errors.New("network error")
		}
		return fake.JWKS(), nil
	})

	// First refresh succeeds.
	err := cache.Refresh()
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", fetchCalls)
	}

	// Get a key by kid (the fake has one key).
	key, err := cache.Get(fake.kid)
	if err != nil {
		t.Fatalf("Get after first refresh: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}

	// Advance past TTL.
	clock.Advance(2 * time.Hour)

	// Make fetch fail.
	failFetch = true

	// Get should still return the stale key.
	key2, err := cache.Get(fake.kid)
	if err != nil {
		t.Fatalf("Get after stale-serve: %v", err)
	}
	if key2 == nil {
		t.Fatal("expected non-nil key on stale-serve")
	}
	if key2 != key {
		t.Error("expected same key pointer on stale-serve")
	}
}

func TestJWKSCache_RefreshesAfterTTL(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)

	fetchCalls := 0
	cache := newJWKSCache(time.Hour, clock, func() ([]byte, error) {
		fetchCalls++
		return fake.JWKS(), nil
	})

	// Initial fetch.
	_, _ = cache.Get(fake.kid)
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d after first Get, want 1", fetchCalls)
	}

	// Within TTL — no refresh.
	clock.Advance(30 * time.Minute)
	_, _ = cache.Get(fake.kid)
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d after Get within TTL, want 1", fetchCalls)
	}

	// Past TTL — should refresh.
	clock.Advance(31 * time.Minute)
	_, _ = cache.Get(fake.kid)
	if fetchCalls != 2 {
		t.Fatalf("fetchCalls = %d after Get past TTL, want 2", fetchCalls)
	}
}

func TestJWKSCache_UnknownKidReturnsError(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)

	cache := newJWKSCache(time.Hour, clock, func() ([]byte, error) {
		return fake.JWKS(), nil
	})

	_, err := cache.Get("unknown-kid")
	if err == nil {
		t.Fatal("expected error for unknown kid, got nil")
	}
}

func TestJWKSCache_SetKeys(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)

	cache := newJWKSCache(time.Hour, clock, func() ([]byte, error) {
		return nil, errors.New("should not be called")
	})

	// Inject keys directly.
	keys := map[string]*rsa.PublicKey{
		fake.kid: &fake.SigningKey.PublicKey,
	}
	cache.SetKeys(keys)

	key, err := cache.Get(fake.kid)
	if err != nil {
		t.Fatalf("Get after SetKeys: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key after SetKeys")
	}
}

func TestParseJWKS_ValidJSON(t *testing.T) {
	clock := NewFakeClock(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC))
	fake := NewFake(clock)

	keys, err := parseJWKS(fake.JWKS())
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}

	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}

	key, ok := keys[fake.kid]
	if !ok {
		t.Fatalf("key with kid %q not found", fake.kid)
	}

	// Verify the parsed key matches the Fake's public key.
	if key.N.Cmp(fake.SigningKey.N) != 0 {
		t.Error("parsed key N does not match Fake's public key N")
	}
	if key.E != fake.SigningKey.E {
		t.Error("parsed key E does not match Fake's public key E")
	}
}

func TestParseJWKS_InvalidJSON(t *testing.T) {
	_, err := parseJWKS([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseJWKS_EmptyKeys(t *testing.T) {
	keys, err := parseJWKS([]byte(`{"keys": []}`))
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}
}
