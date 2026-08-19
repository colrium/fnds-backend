package stytch

import (
	"errors"
	"fmt"
	"time"
)

// Kind classifies every Stytch adapter error into one of six closed categories.
// Callers switch on Kind rather than inspecting status codes or error strings,
// which is what makes the HTTP edge return one code per failure class regardless
// of which adapter call failed (Req 20.4).
type Kind int

const (
	// KindUnavailable means a network error, 5xx, or every attempt exhausted.
	// Maps to 503 identity_provider_unavailable at the HTTP edge.
	KindUnavailable Kind = iota

	// KindRateLimited means Stytch returned 429 and retries were exhausted.
	// Also maps to 503 identity_provider_unavailable.
	KindRateLimited

	// KindNotFound means Stytch returned 404. Used by the orphan report (Req 3.9).
	KindNotFound

	// KindConflict means Stytch returned 409, e.g. slug already taken.
	KindConflict

	// KindInvalidRequest means a 4xx we caused — a bug. Logged at error, surfaced as 500.
	KindInvalidRequest

	// KindUnauthenticated means the credential was rejected. Maps to 401.
	KindUnauthenticated
)

// Error is the adapter's structured error type. Every error returned by the
// adapter is an *Error, so callers can type-assert or use errors.As.
type Error struct {
	// Kind classifies the failure.
	Kind Kind

	// Op is the logical operation name, e.g. "DiscoveryEmailSend".
	Op string

	// Status is the HTTP status code returned by Stytch, or 0 for non-HTTP errors.
	Status int

	// StytchCode is the machine-readable error code from Stytch, if any.
	StytchCode string

	// RetryAfter is the delay Stytch requests on a 429 response. Zero means no
	// Retry-After header was present.
	RetryAfter time.Duration

	// err is the underlying error, if any.
	err error
}

// Error returns a human-readable message. It never includes Config.Secret or a
// session token (Req 20.12).
func (e *Error) Error() string {
	if e.StytchCode != "" {
		return fmt.Sprintf("stytch: %s: %s (status %d, code %s)", e.Op, e.kindString(), e.Status, e.StytchCode)
	}
	if e.Status != 0 {
		return fmt.Sprintf("stytch: %s: %s (status %d)", e.Op, e.kindString(), e.Status)
	}
	if e.err != nil {
		return fmt.Sprintf("stytch: %s: %s: %v", e.Op, e.kindString(), e.err)
	}
	return fmt.Sprintf("stytch: %s: %s", e.Op, e.kindString())
}

// Unwrap returns the underlying error, if any.
func (e *Error) Unwrap() error { return e.err }

func (e *Error) kindString() string {
	switch e.Kind {
	case KindUnavailable:
		return "unavailable"
	case KindRateLimited:
		return "rate_limited"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindInvalidRequest:
		return "invalid_request"
	case KindUnauthenticated:
		return "unauthenticated"
	default:
		return "unknown"
	}
}

// IsUnavailable reports whether err is a Stytch adapter error with Kind
// KindUnavailable or KindRateLimited. Both map to 503 at the HTTP edge.
func IsUnavailable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == KindUnavailable || e.Kind == KindRateLimited
	}
	return false
}

// IsNotFound reports whether err is a Stytch adapter error with Kind
// KindNotFound. Used by the Stytch delete retry job (Req 13.4) to treat
// an already-deleted member as a success.
func IsNotFound(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == KindNotFound
	}
	return false
}
