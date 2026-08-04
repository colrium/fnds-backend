package logging

// Example tests for logging/ingest.go, the only component in this package that
// opens a network connection.
//
// Req 12.3: no test here needs a Better Stack account, a real Source_Token, or
// any network beyond loopback. Every endpoint below is an httptest.Server, and
// assertLoopbackEndpoint makes that a checked claim rather than a convention —
// each test asserts its endpoint resolves to a loopback address before it sends
// anything, so a future edit that points a test at a real host fails here
// instead of silently dialing out. The one deliberately unreachable endpoint,
// in the selftest file's no-secret-leak test, is a closed loopback port.
//
// The header-value, Retry-After, no-secret-leak, and empty-batch cases live in
// ingest_selftest_test.go and are not repeated here.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// assertLoopbackEndpoint fails the test unless raw addresses a loopback host
// (Req 12.3).
func assertLoopbackEndpoint(t *testing.T, raw string) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("test endpoint %q does not parse: %v", raw, err)
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("test endpoint %q is not loopback; tests must not reach the network (Req 12.3)", raw)
	}
}

// connCountingServer is an httptest.Server that records the remote address of
// every request it serves and counts the connections opened to it.
//
// The connection count comes from http.Server.ConnState rather than from
// counting distinct remote addresses: an ephemeral port can be reused by the
// kernel after a close, so distinct-address counting could undercount, while
// StateNew fires exactly once per accepted connection.
type connCountingServer struct {
	*httptest.Server

	mu          sync.Mutex
	remoteAddrs []string
	newConns    int
}

// newConnCountingServer starts a server that responds with status and body to
// every request.
func newConnCountingServer(t *testing.T, status int, body []byte) *connCountingServer {
	t.Helper()

	s := &connCountingServer{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body so the server side of the connection is
		// reusable too; leaving it unread would let the server close the
		// connection and mask a client-side reuse failure.
		_, _ = io.Copy(io.Discard, r.Body)

		s.mu.Lock()
		s.remoteAddrs = append(s.remoteAddrs, r.RemoteAddr)
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	// httptest.Server.Start preserves a ConnState hook set before Start and
	// chains its own bookkeeping onto it, so this must be assigned here.
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		s.mu.Lock()
		s.newConns++
		s.mu.Unlock()
	}
	s.Start()
	t.Cleanup(s.Close)

	assertLoopbackEndpoint(t, s.URL)
	return s
}

func (s *connCountingServer) stats() (remoteAddrs []string, newConns int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.remoteAddrs...), s.newConns
}

// TestIngestClientReusesOneConnectionAcrossAttempts is the point of Req 5.11
// and of the drain in Req 7.16: several consecutive batches travel over a
// single connection, because a fully drained and closed response body returns
// its connection to the idle pool.
//
// The response body here is well under maxResponseBodyBytes, so drainAndClose
// consumes it whole. That is the case reuse depends on.
func TestIngestClientReusesOneConnectionAcrossAttempts(t *testing.T) {
	const attempts = 4
	smallBody := bytes.Repeat([]byte("a"), maxResponseBodyBytes/8)
	server := newConnCountingServer(t, http.StatusAccepted, smallBody)

	// One client for every attempt, which is what Req 5.11 requires: the idle
	// pool lives in the transport, so a client per batch would dial every time.
	client := NewIngestClient(server.URL, "tok", time.Second)
	for i := 0; i < attempts; i++ {
		outcome := client.Send(context.Background(), []*Event{{Message: fmt.Sprintf("attempt-%d", i)}}, 0)
		if outcome.Err != nil {
			t.Fatalf("attempt %d: Send returned Err = %v, want nil", i, outcome.Err)
		}
		if outcome.Status != http.StatusAccepted {
			t.Fatalf("attempt %d: Status = %d, want %d", i, outcome.Status, http.StatusAccepted)
		}
	}

	remoteAddrs, newConns := server.stats()
	if len(remoteAddrs) != attempts {
		t.Fatalf("server served %d requests, want %d", len(remoteAddrs), attempts)
	}
	if newConns != 1 {
		t.Errorf("server accepted %d connections for %d attempts, want 1; a drained body must be pooled (Req 5.11, 7.16)", newConns, attempts)
	}
	for i, addr := range remoteAddrs {
		if addr != remoteAddrs[0] {
			t.Errorf("attempt %d arrived from %s, attempt 0 from %s; every attempt must share one connection", i, addr, remoteAddrs[0])
		}
	}
}

// TestIngestClientOversizeResponseBodyStillReportsStatus covers the deliberate
// trade documented in drainAndClose: a response body longer than
// maxResponseBodyBytes is left partially unread, so net/http closes that
// connection instead of pooling it.
//
// That is intended, not a defect. Req 7.16 bounds the work spent on a response
// this client reads nothing out of, and paying one handshake beats draining an
// unbounded error page. So this test asserts the *absence* of reuse for an
// oversize body — please do not "fix" it into a reuse assertion; the reuse case
// is TestIngestClientReusesOneConnectionAcrossAttempts, and changing the cap
// behaviour should change that trade deliberately, not by making this pass.
//
// What must hold either way: the status still reaches the caller intact.
func TestIngestClientOversizeResponseBodyStillReportsStatus(t *testing.T) {
	const attempts = 2
	oversizeBody := bytes.Repeat([]byte("z"), maxResponseBodyBytes*2)
	server := newConnCountingServer(t, http.StatusRequestEntityTooLarge, oversizeBody)

	client := NewIngestClient(server.URL, "tok", time.Second)
	for i := 0; i < attempts; i++ {
		outcome := client.Send(context.Background(), []*Event{{Message: "x"}}, 0)
		if outcome.Err != nil {
			t.Fatalf("attempt %d: Send returned Err = %v, want nil; an oversize body is still a complete response", i, outcome.Err)
		}
		if outcome.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("attempt %d: Status = %d, want %d", i, outcome.Status, http.StatusRequestEntityTooLarge)
		}
	}

	remoteAddrs, newConns := server.stats()
	if len(remoteAddrs) != attempts {
		t.Fatalf("server served %d requests, want %d", len(remoteAddrs), attempts)
	}
	if newConns != attempts {
		t.Errorf("server accepted %d connections for %d oversize-body attempts, want %d: a body above the %d-byte cap is left unread, so net/http must not pool that connection", newConns, attempts, attempts, maxResponseBodyBytes)
	}
}

// TestDrainAndCloseReadsAtMostCapAndCloses asserts the two halves of Req 7.16
// directly: at most maxResponseBodyBytes are read, and the body is closed
// regardless.
func TestDrainAndCloseReadsAtMostCapAndCloses(t *testing.T) {
	t.Run("body longer than the cap is read only up to the cap", func(t *testing.T) {
		body := &countingBody{remaining: maxResponseBodyBytes * 3}
		drainAndClose(body)

		if body.read != maxResponseBodyBytes {
			t.Errorf("read %d bytes, want exactly %d", body.read, maxResponseBodyBytes)
		}
		if !body.closed {
			t.Error("body was not closed")
		}
	})

	t.Run("body shorter than the cap is read whole", func(t *testing.T) {
		body := &countingBody{remaining: 128}
		drainAndClose(body)

		if body.read != 128 {
			t.Errorf("read %d bytes, want all 128 so the connection can be pooled", body.read)
		}
		if !body.closed {
			t.Error("body was not closed")
		}
	})

	t.Run("nil body is tolerated", func(t *testing.T) {
		drainAndClose(nil) // a nil body must not panic the delivery goroutine
	})
}

// countingBody is a response body of a fixed length that records how much of
// itself was read and whether it was closed.
type countingBody struct {
	remaining int
	read      int
	closed    bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > b.remaining {
		n = b.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	b.remaining -= n
	b.read += n
	return n, nil
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

// TestIngestClientSendReportsTimeout covers both deadline sources of Req 5.10
// and 9.6: the client timeout fixed at construction, and the perRequestTimeout
// argument Close uses when little of its budget remains. Either one must abort
// the request and surface a timeout, not a status.
//
// The server blocks on a channel instead of sleeping, so the only real elapsed
// time is the timeout itself, far inside the 50 ms ceiling of Req 12.2.
func TestIngestClientSendReportsTimeout(t *testing.T) {
	const timeout = 20 * time.Millisecond

	tests := []struct {
		name              string
		clientTimeout     time.Duration
		perRequestTimeout time.Duration
	}{
		{
			name:          "client timeout",
			clientTimeout: timeout,
		},
		{
			name:              "per-request timeout",
			clientTimeout:     10 * time.Second, // long enough that only the argument can fire
			perRequestTimeout: timeout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Returns as soon as the client gives up, so Close never waits.
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(func() {
				close(release)
				server.Close()
			})
			assertLoopbackEndpoint(t, server.URL)

			outcome := NewIngestClient(server.URL, "tok", tc.clientTimeout).
				Send(context.Background(), []*Event{{Message: "x"}}, tc.perRequestTimeout)

			if outcome.Err == nil {
				t.Fatalf("Send returned Err = nil for a request that outlived its %v deadline", timeout)
			}
			if outcome.Status != 0 {
				t.Errorf("Status = %d, want 0 when no response was received", outcome.Status)
			}
			if !isTimeout(outcome.Err) {
				t.Errorf("isTimeout(%v) = false, want true", outcome.Err)
			}
			if !strings.Contains(outcome.Err.Error(), "timed out") {
				t.Errorf("Err = %q, want it to name the timeout so Req 7.7 can report the category", outcome.Err)
			}
		})
	}
}

// TestIngestClientSendReportsEveryStatusVerbatim pins the division of labour:
// this client attaches no meaning to a status. Every response is reported as it
// arrived, with a nil Err, and classification — success, permanent failure,
// throttling, permanent disable — belongs to the Shipper (task 8.1).
func TestIngestClientSendReportsEveryStatusVerbatim(t *testing.T) {
	statuses := []int{
		http.StatusAccepted,              // 202 success
		http.StatusPaymentRequired,       // 402 non-retryable
		http.StatusForbidden,             // 403 permanent disable, per Req 7.8
		http.StatusNotAcceptable,         // 406 non-retryable
		http.StatusRequestEntityTooLarge, // 413 non-retryable
		http.StatusTooManyRequests,       // 429 retryable
		http.StatusInternalServerError,   // 5xx retryable
		http.StatusServiceUnavailable,    // 5xx retryable
	}

	for _, status := range statuses {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			assertLoopbackEndpoint(t, server.URL)

			outcome := NewIngestClient(server.URL, "tok", time.Second).
				Send(context.Background(), []*Event{{Message: "x"}}, 0)

			if outcome.Err != nil {
				t.Fatalf("Send returned Err = %v for status %d, want nil; a response is not a transport failure", outcome.Err, status)
			}
			if outcome.Status != status {
				t.Errorf("Status = %d, want %d reported verbatim", outcome.Status, status)
			}
			if outcome.RetryAfter != 0 {
				t.Errorf("RetryAfter = %v, want 0 with no Retry-After header", outcome.RetryAfter)
			}
		})
	}
}

// TestIngestClientSendHonorsCancelledContext: a caller-cancelled context aborts
// the attempt the same way a timeout does — no status, an error the Shipper can
// treat as a transport failure.
func TestIngestClientSendHonorsCancelledContext(t *testing.T) {
	var mu sync.Mutex
	var served int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		served++
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	assertLoopbackEndpoint(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := NewIngestClient(server.URL, "tok", time.Second).
		Send(ctx, []*Event{{Message: "x"}}, 0)

	if outcome.Err == nil {
		t.Fatal("Send returned Err = nil for a cancelled context, want a failure")
	}
	if outcome.Status != 0 {
		t.Errorf("Status = %d, want 0 when no response was received", outcome.Status)
	}
	if !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("Err = %v, want it to wrap context.Canceled", outcome.Err)
	}

	mu.Lock()
	defer mu.Unlock()
	if served != 0 {
		t.Errorf("server served %d requests for a cancelled context, want 0", served)
	}
}
