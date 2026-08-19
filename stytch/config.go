package stytch

import "time"

// Config is injected by main.go from configs.Stytch*(). The adapter reads no
// environment variable, following the logging.RawConfig precedent, so every
// test constructs an adapter without t.Setenv.
type Config struct {
	// ProjectID is the Stytch project identifier.
	ProjectID string

	// Secret is the Stytch project secret used for API authentication.
	Secret string

	// PublicToken is the Stytch public token for client-side SDK usage.
	PublicToken string

	// Environment is "test" or "live".
	Environment string

	// WebhookSecret is the whsec_-prefixed HMAC key for webhook verification.
	WebhookSecret string

	// BaseURL overrides the SDK default base URL. Empty means the SDK default.
	// Tests set this to an httptest.Server URL.
	BaseURL string

	// Timeout is the per-attempt timeout. Default and maximum is 5s.
	Timeout time.Duration

	// JWKSTTL is the minimum cache duration for the JWKS key set. Minimum 1h.
	JWKSTTL time.Duration

	// JWKSFetchTimeout bounds a single JWKS document fetch. The fetch runs
	// under the JWKS cache write lock, so this is a ceiling on how long a cold
	// cache miss can block session verification platform-wide. Zero, or
	// anything above maxJWKSFetchTimeout (30s), means
	// defaultJWKSFetchTimeout (10s); high-latency or cross-region deployments
	// can raise it within that bound.
	JWKSFetchTimeout time.Duration

	// MaxAttempts is the maximum number of attempts per operation. Default 3.
	MaxAttempts int

	// Clock is the injected time source. Tests advance it explicitly.
	Clock Clock
}
