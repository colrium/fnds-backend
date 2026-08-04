package logging

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"
)

// This file deliberately imports the standard library only. In particular it
// must never import echo: contextutil imports logging so that
// contextutil.SetUser can write the authenticated user id into the stdlib
// context, and an echo import here would create a cycle. slog.Handler.Handle
// receives a context.Context, not an echo.Context, so correlation values must
// live in the stdlib context to be visible to the Better_Stack_Handler.

// Context keys are unexported zero-size struct types rather than strings, so a
// value written here can never be read or overwritten by another package that
// happens to use the same string key.
type requestContextKey struct{}

type userIDContextKey struct{}

// requestInfo is the correlation triple carried by a request context. It is
// stored as one value so a single context lookup yields all three fields.
type requestInfo struct {
	id     string
	method string
	path   string
}

// ContextWithRequest returns a copy of ctx carrying the request correlation
// triple: the assigned request identifier, the HTTP method, and the registered
// route template with no path parameter substituted (Req 4.5, 4.8).
//
// The values are stored byte-for-byte, so a client-supplied identifier adopted
// by the middleware reaches the Log_Event unmodified (Req 4.3).
//
// An empty id is treated as "no request identifier": ctx is returned unchanged
// so that RequestFromContext reports ok == false and the handler omits the
// request object rather than emitting an empty id (Req 4.7).
func ContextWithRequest(ctx context.Context, id, method, path string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestContextKey{}, requestInfo{id: id, method: method, path: path})
}

// ContextWithUserID returns a copy of ctx carrying the authenticated user
// identifier (Req 4.6).
//
// An empty id is treated as "not authenticated": ctx is returned unchanged so
// that the handler omits the user object (Req 4.10).
func ContextWithUserID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, userIDContextKey{}, id)
}

// RequestFromContext returns the request correlation triple carried by ctx.
// ok is false when ctx carries no request identifier, in which case the caller
// emits a Log_Event with the request object omitted (Req 4.7).
//
// Every Log_Record emitted with a context derived from one request's context
// observes that request's single identifier, which is what makes all such
// Log_Events share an identical request.id (Req 4.11).
func RequestFromContext(ctx context.Context) (id, method, path string, ok bool) {
	if ctx == nil {
		return "", "", "", false
	}
	info, ok := ctx.Value(requestContextKey{}).(requestInfo)
	if !ok {
		return "", "", "", false
	}
	return info.id, info.method, info.path, true
}

// UserIDFromContext returns the authenticated user identifier carried by ctx.
// ok is false when no user identifier is present, which is the normal case on
// public routes (Req 4.10).
func UserIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(userIDContextKey{}).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// FromContext returns slog.Default() with the correlation values found in ctx
// bound as attributes.
//
// It is sugar for call sites that prefer holding a logger over passing ctx to
// every call. Correlation does not depend on it: the Better_Stack_Handler reads
// the context passed to Handle directly, so slog.ErrorContext(ctx, ...) is
// correlated whether or not this helper is used. When ctx carries no
// correlation values the default logger is returned unchanged.
func FromContext(ctx context.Context) *slog.Logger {
	logger := slog.Default()
	if ctx == nil {
		return logger
	}

	attrs := make([]any, 0, 2)
	if id, method, path, ok := RequestFromContext(ctx); ok {
		attrs = append(attrs, slog.Group("request",
			slog.String("id", id),
			slog.String("method", method),
			slog.String("path", path),
		))
	}
	if userID, ok := UserIDFromContext(ctx); ok {
		attrs = append(attrs, slog.Group("user", slog.String("id", userID)))
	}
	if len(attrs) == 0 {
		return logger
	}
	return logger.With(attrs...)
}

// nonceBytes is the size of the per-process random nonce that prefixes every
// generated request identifier. 16 bytes is 128 bits of entropy, which makes a
// collision between two running process instances not worth reasoning about.
const nonceBytes = 16

// processNonce is generated exactly once per process. Generating it at init
// rather than per request keeps NewRequestID allocation-light and off the
// crypto/rand path on the request hot line.
var processNonce = newProcessNonce()

// requestSeq is the monotonic counter appended to processNonce. It is what
// makes distinctness within the process guaranteed rather than probabilistic
// (Req 4.1): two calls cannot observe the same counter value, so two generated
// identifiers cannot be equal. atomic.Uint64 makes NewRequestID safe for
// concurrent use from every request-serving goroutine.
var requestSeq atomic.Uint64

func newProcessNonce() string {
	b := make([]byte, nonceBytes)
	if _, err := crand.Read(b); err != nil {
		// crypto/rand.Read is not expected to fail. Logging is never allowed to
		// be load-bearing, so a failure degrades the cross-process uniqueness of
		// the nonce instead of panicking during package initialisation. Within
		// this process, uniqueness still holds by the counter alone (Req 4.1).
		n := uint64(time.Now().UnixNano())
		for i := range b {
			b[i] = byte(n)
			// splitmix64-style advance: cheap, stdlib-free, and good enough for a
			// fallback that only needs to differ between processes.
			n = n*6364136223846793005 + 1442695040888963407
		}
	}
	return hex.EncodeToString(b)
}

// NewRequestID returns an identifier that is distinct from the identifier
// returned by every other call within this process (Req 4.1).
//
// Format: hex(process nonce) + "-" + base-36 monotonic counter. The charset is
// ASCII alphanumeric plus '-', and the length is 34 to 46 characters, both
// inside the bounds Req 4.1 states and accepted by ValidRequestID.
//
// Safe for concurrent use.
func NewRequestID() string {
	return processNonce + "-" + strconv.FormatUint(requestSeq.Add(1), 36)
}

// maxRequestIDLen is the longest client-supplied identifier that may be adopted
// (Req 4.3). Longer values are discarded and replaced (Req 4.4).
const maxRequestIDLen = 200

// ValidRequestID reports whether a client-supplied X-Request-Id value may be
// adopted byte-for-byte: 1 to 200 characters drawn only from ASCII
// alphanumerics, '-', and '_' (Req 4.3).
//
// Only ASCII is legal, so comparing the byte length against the 200-character
// bound is exact for every accepted value, and any multi-byte sequence is
// rejected by the charset check regardless of length. A false result means the
// middleware discards the supplied value, generates a replacement, and
// continues without erroring (Req 4.4).
func ValidRequestID(s string) bool {
	if len(s) == 0 || len(s) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
