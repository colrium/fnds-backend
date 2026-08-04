package logging

// Ingest_Client: the only component in this package that opens a network
// connection.
//
// It is structured after notifications/slack.go, the existing stdlib-only HTTP
// sink in this repository: marshal the body with encoding/json, POST it with
// net/http, close the response body, return a wrapped error (Req 12.6).
//
// Deliberately absent, because they belong to the Shipper and not here:
// batching (task 6.5), status classification (task 8.1), and retry, backoff,
// and the Retry-After clamp (task 8.2). Send transmits exactly one batch
// exactly once and reports what happened. It makes no decision about what the
// result means, which is what keeps the delivery state machine in one place.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	// DefaultRequestTimeout is the per-request timeout covering connection,
	// transmission, and response reading (Req 5.10). It is the value
	// NewIngestClient falls back to when it is handed a non-positive timeout,
	// so that no IngestClient can ever be constructed without a deadline.
	DefaultRequestTimeout = 10 * time.Second

	// maxResponseBodyBytes is the cap on how much of an ingest response body is
	// read before the body is closed (Req 7.16). Better Stack's responses carry
	// no field this client acts on — the status line and Retry-After are the
	// whole contract — so the read exists only to let the connection return to
	// the pool, and 4 KiB is enough to consume a typical error body whole.
	maxResponseBodyBytes = 4096

	// maxRetryAfterSeconds bounds a parsed Retry-After value at one day.
	//
	// This is arithmetic safety, not the Req 7.4 clamp: seconds are multiplied
	// by time.Second, and an absurd header value such as 10^12 would overflow
	// int64 nanoseconds and wrap to a negative duration, which reads back as
	// "no Retry-After" and would silently discard a real header. Any value at
	// or above 300 collapses to the same 300 s once retryDelay clamps it, so
	// this cap changes no observable retry delay.
	maxRetryAfterSeconds = 86400
)

// IngestClient POSTs Delivery_Batches to the Ingest_Endpoint.
//
// One instance holds exactly one *http.Client for the lifetime of the process
// (Req 5.11), which is what makes idle connection reuse possible: the transport
// is where Go's connection pool lives, so a client constructed per request
// would open a fresh TLS connection for every batch.
type IngestClient struct {
	url   string
	token string
	http  *http.Client // exactly one instance for the process (Req 5.11)
}

// SendOutcome is the complete result of one transmission attempt.
//
// Exactly one of the two failure signals is meaningful at a time:
//
//   - Status > 0 means a response arrived and Err is nil. The caller classifies
//     the status (task 8.1); this client attaches no meaning to it.
//   - Status == 0 means no response was received and Err describes why: a
//     transport-level failure or an elapsed timeout (Req 5.10).
//
// RetryAfter carries the raw Retry-After value of the response, unclamped. The
// 1–300 s clamp of Req 7.4 lives in the Shipper's retryDelay (task 8.2),
// together with the doubling schedule and the jitter it has to compose with.
// Splitting it that way keeps every rule that shapes a delay in one function
// and leaves this client with no retry policy at all.
type SendOutcome struct {
	Status     int // 0 when no response was received
	RetryAfter time.Duration
	Err        error // transport-level failure or timeout
}

// NewIngestClient returns a client that POSTs to url, authenticating with
// token.
//
// timeout becomes the http.Client timeout and covers connection, transmission,
// and response reading (Req 5.10). A non-positive timeout is replaced by
// DefaultRequestTimeout rather than accepted: http.Client treats zero as "no
// timeout at all", and a delivery goroutine blocked forever on a black-holed
// connection would stop shipping for the lifetime of the process.
//
// Transport is left nil on purpose, which selects http.DefaultTransport and
// with it the shared idle connection pool and keep-alives (Req 5.10, 5.11).
// Naming a fresh &http.Transport{} here would give this client a private pool
// with no measurable benefit, and setting DisableKeepAlives would defeat
// Req 5.11 outright.
func NewIngestClient(url, token string, timeout time.Duration) *IngestClient {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return &IngestClient{
		url:   url,
		token: token,
		http: &http.Client{
			Timeout: timeout,
		},
	}
}

// Send transmits one Delivery_Batch as a single HTTP POST and reports the
// outcome. It performs exactly one attempt: it never retries, and it never
// waits.
//
// The body is a JSON array of the events in slice order, which is enqueue order
// (Req 5.8). Headers are Content-Type: application/json and Authorization:
// Bearer <token> (Req 5.9).
//
// perRequestTimeout shortens the deadline for this one attempt, which is what
// Close needs when less than the 10 s per-request timeout remains in its 5 s
// budget (Req 9.6). The effective deadline is the lesser of perRequestTimeout
// and the client timeout without any comparison here, because both are
// enforced: the context deadline fires first when it is shorter, and the client
// timeout fires first when it is not. A non-positive perRequestTimeout leaves
// the client timeout as the only deadline.
//
// An empty batch is a no-op: it returns the zero SendOutcome having opened no
// connection, because a request carrying no event has nothing to deliver
// (Req 5.12). Callers must not read that zero value as a transport failure. The
// Shipper never calls Send with an empty batch; the guard exists so that a
// future caller bug costs a wasted call rather than a malformed request.
func (c *IngestClient) Send(ctx context.Context, batch []*Event, perRequestTimeout time.Duration) SendOutcome {
	if len(batch) == 0 {
		return SendOutcome{}
	}

	body, err := json.Marshal(batch)
	if err != nil {
		// Unreachable for events produced by convert, whose fields are all
		// JSON-representable by construction (Req 3.13). Reported rather than
		// ignored so that a future field carrying an unmarshalable type
		// surfaces as one failed batch instead of silent loss.
		return SendOutcome{Err: fmt.Errorf("failed to marshal betterstack ingest batch of %d events: %w", len(batch), err)}
	}

	if perRequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, perRequestTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return SendOutcome{Err: c.wrapSendError(err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		// Both a timeout and a dial or TLS failure land here, and both are
		// retryable by Req 7.3, so both travel in Err. They differ only in the
		// wrapped text, which Req 7.7's "transport failure category" reports.
		return SendOutcome{Err: c.wrapSendError(err)}
	}

	// Req 7.16, and the ordering is the whole point: the status and the
	// Retry-After header are already in memory once Do returns, so they are
	// read out here and the body is drained and closed *before* this function
	// returns — which is before any caller can inspect Status, on every status
	// and on every attempt. That is what returns the connection to the idle
	// pool. A deferred Close would technically also run before the return, but
	// it reads as an afterthought that a later edit could reorder past an early
	// return; there is no early return between Do and drainAndClose, so the
	// explicit call is both correct and self-evident.
	outcome := SendOutcome{
		Status:     resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	drainAndClose(resp.Body)
	return outcome
}

// drainAndClose reads at most maxResponseBodyBytes of body, discards them, and
// closes body (Req 7.16).
//
// Both errors are ignored on purpose. io.CopyN reports io.EOF for every body
// shorter than the cap, which is the common case and not a failure, and a Close
// error on a response this client has already extracted everything it needs
// from carries no information any caller could act on. Nothing here can change
// the delivered/dropped accounting of the batch.
//
// A body longer than the cap is left partially unread, so net/http closes that
// connection instead of pooling it. That is the deliberate trade in Req 7.16:
// bounded work per response, rather than draining an unbounded error page to
// save a handshake.
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, body, maxResponseBodyBytes)
	_ = body.Close()
}

// parseRetryAfter converts a Retry-After header value into a duration, or
// returns 0 when the header is absent or unusable.
//
// Only the delta-seconds form is supported, per Req 7.4: a non-negative base-10
// integer count of seconds. RFC 9110 also allows an HTTP-date, and this client
// deliberately does not implement it — Better Stack sends delta-seconds, an
// HTTP-date would need a trusted clock to convert while every duration in this
// package comes from the injected Clock (Req 12.2), and the fallback is
// harmless: an unrecognised value yields 0, and the Shipper then uses its
// doubling backoff schedule.
//
// A negative value is rejected the same way. net/http has already stripped the
// optional whitespace around a header value, so no trimming is needed here.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}
	if seconds > maxRetryAfterSeconds {
		seconds = maxRetryAfterSeconds
	}
	return time.Duration(seconds) * time.Second
}

// wrapSendError turns a net/http failure into an error that is safe to log.
//
// Two secrets must not reach a diagnostic. The Source_Token never can: it is
// only ever a header value, and net/http puts no header in an error (Req 10.4).
// The endpoint can: *url.Error stringifies the full request URL, including any
// query string and any userinfo. So the *url.Error wrapper is unwrapped away
// and replaced with a sanitised endpoint, leaving scheme, host, and path only
// (Req 10.5). Errors below that wrapper — net.OpError, DNS, TLS — name a host
// and port, which Req 10.5 permits.
func (c *IngestClient) wrapSendError(err error) error {
	inner := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		inner = urlErr.Err
	}
	if isTimeout(err) {
		return fmt.Errorf("betterstack ingest POST to %s timed out: %w", sanitizeEndpoint(c.url), inner)
	}
	return fmt.Errorf("failed to POST betterstack ingest batch to %s: %w", sanitizeEndpoint(c.url), inner)
}

// isTimeout reports whether err is an elapsed deadline rather than another
// transport failure.
//
// Both sources are checked because both can fire: the context deadline from
// perRequestTimeout (Req 9.6) surfaces as context.DeadlineExceeded, while the
// http.Client timeout (Req 5.10) surfaces as an error whose Timeout method
// reports true. Req 7.3 retries either one identically; the distinction only
// sharpens the failure category Req 7.7 reports.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeoutErr interface{ Timeout() bool }
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}

// sanitizeEndpoint renders an endpoint as scheme, host, and path only,
// dropping the query string and any userinfo (Req 10.5).
//
// An unparseable endpoint yields a fixed placeholder rather than the raw
// string, because the reason it failed to parse may be the very credential
// material this function exists to withhold. ResolveConfig rejects an
// unparseable URL before a Shipper is ever enabled (Req 1.6), so in production
// this path is unreachable; it protects a client constructed directly.
func sanitizeEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "the configured ingest endpoint"
	}
	safe := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}
	return safe.String()
}
