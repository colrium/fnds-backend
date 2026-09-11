package stytch

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	stytchOrganizationClaim = "https://stytch.com/organization"
	stytchSessionClaim      = "https://stytch.com/session"
)

// jwksCache holds the cached JWKS keys and manages refresh.
type jwksCache struct {
	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey // kid -> public key
	lastFetch time.Time
	ttl       time.Duration
	fetchFn   func() ([]byte, error) // injected: how to fetch JWKS
	clock     Clock
}

// newJWKSCache creates a JWKS cache with the given TTL, clock, and fetch function.
func newJWKSCache(ttl time.Duration, clock Clock, fetchFn func() ([]byte, error)) *jwksCache {
	return &jwksCache{
		keys:    make(map[string]*rsa.PublicKey),
		ttl:     ttl,
		fetchFn: fetchFn,
		clock:   clock,
	}
}

// Get returns the RSA public key for the given kid, refreshing the cache if needed.
// If refresh fails, the last successfully fetched keys are returned (Req 20.6).
func (c *jwksCache) Get(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	expired := c.clock.Now().Sub(c.lastFetch) >= c.ttl
	key, ok := c.keys[kid]
	c.mu.RUnlock()

	if ok && !expired {
		return key, nil
	}

	// Need to refresh (or key not found). Upgrade to write lock.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	expired = c.clock.Now().Sub(c.lastFetch) >= c.ttl
	if !expired {
		if k, found := c.keys[kid]; found {
			return k, nil
		}
	}

	// Attempt refresh.
	if expired || !ok {
		_ = c.refreshLocked()
	}

	// Return whatever we have now (stale-serve on refresh failure).
	if k, found := c.keys[kid]; found {
		return k, nil
	}

	return nil, fmt.Errorf("stytch: JWKS: key ID %q not found", kid)
}

// Refresh attempts to fetch and parse the JWKS. If it fails, the old keys are kept.
func (c *jwksCache) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshLocked()
}

// refreshLocked performs the actual JWKS fetch and parse. Caller must hold c.mu.
func (c *jwksCache) refreshLocked() error {
	data, err := c.fetchFn()
	if err != nil {
		// Stale-serve: keep the old keys (Req 20.6).
		return err
	}

	keys, err := parseJWKS(data)
	if err != nil {
		return err
	}

	c.keys = keys
	c.lastFetch = c.clock.Now()
	return nil
}

// SetKeys allows tests to inject keys directly (used by the Fake).
func (c *jwksCache) SetKeys(keys map[string]*rsa.PublicKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = keys
	c.lastFetch = c.clock.Now()
}

// jwksJSON is the standard JWKS JSON format.
type jwksJSON struct {
	Keys []jwkJSON `json:"keys"`
}

// jwkJSON is one key in the JWKS.
type jwkJSON struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// parseJWKS parses a standard JWKS JSON document and returns a map of kid -> *rsa.PublicKey.
func parseJWKS(data []byte) (map[string]*rsa.PublicKey, error) {
	var jwks jwksJSON
	if err := json.Unmarshal(data, &jwks); err != nil {
		return nil, fmt.Errorf("stytch: JWKS: parse: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		if k.Kid == "" {
			continue
		}

		pubKey, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			return nil, fmt.Errorf("stytch: JWKS: key %q: %w", k.Kid, err)
		}
		keys[k.Kid] = pubKey
	}

	return keys, nil
}

// parseRSAPublicKey converts base64url-encoded n and e into an *rsa.PublicKey.
func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	if !e.IsInt64() {
		return nil, fmt.Errorf("exponent too large")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// VerifySessionJWT verifies a Stytch session JWT locally against the cached JWKS.
// It never calls Stytch, so it is the path that survives an outage (Req 20.5).
// Returns a Session on success with MemberID, OrganizationID, SessionID, ExpiresAt populated.
func (a *Adapter) VerifySessionJWT(tokenStr string) (Session, error) {
	if a.jwks == nil {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  errors.New("JWKS cache not initialized"),
		}
	}

	// Parse without verification first to extract the kid from the header.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  fmt.Errorf("parse JWT header: %w", err),
		}
	}

	kid, ok := unverified.Header["kid"].(string)
	if !ok || kid == "" {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  errors.New("JWT missing kid header"),
		}
	}

	// Look up the public key from the JWKS cache.
	pubKey, err := a.jwks.Get(kid)
	if err != nil {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  fmt.Errorf("JWKS lookup: %w", err),
		}
	}

	// Verify the JWT signature and standard claims (exp check with clock).
	now := a.cfg.Clock.Now()
	verifyParser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)

	token, err := verifyParser.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		return pubKey, nil
	})
	if err != nil {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  fmt.Errorf("verify JWT: %w", err),
		}
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  errors.New("invalid JWT claims"),
		}
	}

	// Stytch's production B2B session JWTs put organization and session
	// metadata in namespaced objects. Keep the legacy flat claim fallback for
	// tokens minted by older Fundis test fixtures and any sessions issued
	// before this verifier was corrected.
	memberID, _ := claims["sub"].(string)
	orgID := nestedStringClaim(claims, stytchOrganizationClaim, "organization_id")
	if orgID == "" {
		orgID, _ = claims["org_id"].(string)
	}
	iss, _ := claims["iss"].(string)

	// Validate issuer matches expected pattern.
	expectedIssuerPrefix := "stytch.com/" + a.cfg.ProjectID
	if iss != expectedIssuerPrefix {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  fmt.Errorf("issuer mismatch: got %q, want %q", iss, expectedIssuerPrefix),
		}
	}

	if memberID == "" {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  errors.New("JWT missing sub claim"),
		}
	}

	if orgID == "" {
		return Session{}, &Error{
			Kind: KindUnauthenticated,
			Op:   "VerifySessionJWT",
			err:  errors.New("JWT missing org_id claim"),
		}
	}

	// Extract expiry.
	var expiresAt time.Time
	if exp, ok := claims["exp"].(float64); ok {
		expiresAt = time.Unix(int64(exp), 0)
	}

	// Extract session ID if present.
	sessionID := nestedStringClaim(claims, stytchSessionClaim, "id")
	if sessionID == "" {
		sessionID, _ = claims["session_id"].(string)
	}

	return Session{
		MemberID:       memberID,
		OrganizationID: orgID,
		SessionID:      sessionID,
		ExpiresAt:      expiresAt,
	}, nil
}

func nestedStringClaim(claims jwt.MapClaims, namespace, key string) string {
	raw, ok := claims[namespace]
	if !ok {
		return ""
	}

	switch nested := raw.(type) {
	case map[string]any:
		value, _ := nested[key].(string)
		return value
	case jwt.MapClaims:
		value, _ := nested[key].(string)
		return value
	default:
		return ""
	}
}
