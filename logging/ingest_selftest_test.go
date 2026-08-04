package logging

// Self-verification for logging/ingest.go, kept in its own file so that task
// 5.2 can create logging/ingest_test.go without a merge conflict. These cover
// the three behaviours that are easiest to get silently wrong; 5.2 owns the
// fuller example suite, including the connection-reuse assertion.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Req 5.8, 5.9: one POST, a JSON array in enqueue order, and the exact header
// values.
func TestIngestClientSendPostsBatchWithHeaders(t *testing.T) {
	type received struct {
		method string
		ctype  string
		auth   string
		body   []byte
	}
	got := make(chan received, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		got <- received{
			method: r.Method,
			ctype:  r.Header.Get("Content-Type"),
			auth:   r.Header.Get("Authorization"),
			body:   body[:n],
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewIngestClient(server.URL, "tok-123", time.Second)
	batch := []*Event{
		{DT: "2024-01-01T00:00:00.000Z", Level: "ERROR", Message: "first"},
		{DT: "2024-01-01T00:00:01.000Z", Level: "ERROR", Message: "second"},
	}

	outcome := client.Send(context.Background(), batch, 0)
	if outcome.Err != nil {
		t.Fatalf("Send returned Err = %v, want nil", outcome.Err)
	}
	if outcome.Status != http.StatusAccepted {
		t.Errorf("Status = %d, want %d", outcome.Status, http.StatusAccepted)
	}

	req := <-got
	if req.method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.method)
	}
	if req.ctype != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.ctype)
	}
	if req.auth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want %q", req.auth, "Bearer tok-123")
	}

	var decoded []Event
	if err := json.Unmarshal(req.body, &decoded); err != nil {
		t.Fatalf("body is not a JSON array: %v (body %q)", err, req.body)
	}
	if len(decoded) != 2 || decoded[0].Message != "first" || decoded[1].Message != "second" {
		t.Errorf("body = %q, want the two events in enqueue order", req.body)
	}
}

// Req 7.4: the raw Retry-After value reaches the caller unclamped, so that the
// 1-300 s clamp stays in the Shipper's retryDelay. An unusable value yields 0.
func TestIngestClientSendParsesRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "seconds", header: "7", want: 7 * time.Second},
		{name: "above the shipper clamp is left unclamped", header: "600", want: 600 * time.Second},
		{name: "zero", header: "0", want: 0},
		{name: "absent", header: "", want: 0},
		{name: "negative", header: "-5", want: 0},
		{name: "http-date is unsupported", header: "Wed, 21 Oct 2015 07:28:00 GMT", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()

			outcome := NewIngestClient(server.URL, "tok", time.Second).
				Send(context.Background(), []*Event{{Message: "x"}}, 0)

			if outcome.Status != http.StatusTooManyRequests {
				t.Fatalf("Status = %d, want 429", outcome.Status)
			}
			if outcome.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", outcome.RetryAfter, tc.want)
			}
		})
	}
}

// Req 10.4, 10.5: a transport failure is reported without the Source_Token, the
// endpoint query string, or the endpoint userinfo.
func TestIngestClientSendErrorLeaksNoSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host := strings.TrimPrefix(server.URL, "http://")
	server.Close() // guarantees a refused connection on a port nothing listens on

	const token = "super-secret-token"
	endpoint := "http://user:hunter2@" + host + "/ingest?source_token=" + token

	outcome := NewIngestClient(endpoint, token, time.Second).
		Send(context.Background(), []*Event{{Message: "x"}}, 0)

	if outcome.Err == nil {
		t.Fatal("Send returned nil Err for a refused connection, want a transport failure")
	}
	if outcome.Status != 0 {
		t.Errorf("Status = %d, want 0 when no response was received", outcome.Status)
	}
	for _, secret := range []string{token, "hunter2", "source_token"} {
		if strings.Contains(outcome.Err.Error(), secret) {
			t.Errorf("Err %q contains %q", outcome.Err, secret)
		}
	}
	if !strings.Contains(outcome.Err.Error(), host) {
		t.Errorf("Err %q omits the host %q, which Req 10.5 permits and a diagnostic needs", outcome.Err, host)
	}
}

// Req 5.12: an empty batch opens no connection.
func TestIngestClientSendEmptyBatchMakesNoRequest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	outcome := NewIngestClient(server.URL, "tok", time.Second).Send(context.Background(), nil, 0)

	if outcome != (SendOutcome{}) {
		t.Errorf("Send(nil) = %+v, want the zero SendOutcome", outcome)
	}
	if calls != 0 {
		t.Errorf("server saw %d requests for an empty batch, want 0", calls)
	}
}
