package logging

// Example tests for logging/classify.go: one row per boundary of Req 7's status
// table, plus the three shapes of SendOutcome that carry no status.
//
// These are deliberately example-based and enumerate boundaries only. Property 11
// (task 8.4) generalises the same table over arbitrary statuses against an oracle
// derived from the requirement's ranges; the names here are prefixed TestClassify
// so that file can be added with no collision.
//
// Two rows exercise statuses no HTTP server can deliver — 199, which net/http
// treats as informational and never surfaces as a final status, and 600, which is
// not transmittable at all. They are reachable only because classify is a pure
// function of a SendOutcome rather than something that has to be provoked out of
// an httptest.Server, which is the point of keeping it pure.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"
)

// fakeTimeoutError is a transport error that reports itself as a timeout the way
// net/http's does, through a Timeout method rather than through
// context.DeadlineExceeded. Both routes reach isTimeout, and Req 7.3 retries
// either identically, so both are exercised.
type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string { return "simulated i/o timeout" }
func (fakeTimeoutError) Timeout() bool { return true }

// TestClassifyStatusTable walks every boundary of the design's classification
// table (Req 7.1, 7.2, 7.3, 7.10, 7.11, 7.14, 7.15).
func TestClassifyStatusTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		outcome   SendOutcome
		want      deliveryOutcome
		wantDiag  diagRequirement
		wantLevel slog.Level // checked only when wantDiag != diagNone
	}{
		// Below 200: Req 7.15's complement, at the boundary net/http cannot
		// deliver.
		{name: "199 informational", outcome: SendOutcome{Status: 199}, want: outcomeDrop, wantDiag: diagErrorStatus, wantLevel: slog.LevelError},

		// 2xx: delivered either way, WARN for everything but 202 (Req 7.1, 7.14).
		{name: "200 lower 2xx boundary", outcome: SendOutcome{Status: 200}, want: outcomeDelivered, wantDiag: diagWarnStatus, wantLevel: slog.LevelWarn},
		{name: "202 accepted", outcome: SendOutcome{Status: 202}, want: outcomeDelivered, wantDiag: diagNone},
		{name: "299 upper 2xx boundary", outcome: SendOutcome{Status: 299}, want: outcomeDelivered, wantDiag: diagWarnStatus, wantLevel: slog.LevelWarn},

		// Just outside 2xx, and a plain 4xx: both fall to Req 7.15.
		{name: "300 just above 2xx", outcome: SendOutcome{Status: 300}, want: outcomeDrop, wantDiag: diagErrorStatus, wantLevel: slog.LevelError},
		{name: "400 bad request", outcome: SendOutcome{Status: 400}, want: outcomeDrop, wantDiag: diagErrorStatus, wantLevel: slog.LevelError},
		{name: "499 other 4xx", outcome: SendOutcome{Status: 499}, want: outcomeDrop, wantDiag: diagErrorStatus, wantLevel: slog.LevelError},

		// The four named non-retryable statuses.
		{name: "402 payment required suspends", outcome: SendOutcome{Status: 402}, want: outcomeSuspend, wantDiag: diagErrorQuota, wantLevel: slog.LevelError},
		{name: "403 forbidden disables", outcome: SendOutcome{Status: 403}, want: outcomeDisable, wantDiag: diagErrorToken, wantLevel: slog.LevelError},
		{name: "406 not acceptable", outcome: SendOutcome{Status: 406}, want: outcomeDrop, wantDiag: diagErrorStatusCount, wantLevel: slog.LevelError},
		{name: "413 payload too large", outcome: SendOutcome{Status: 413}, want: outcomeDrop, wantDiag: diagErrorStatusCount, wantLevel: slog.LevelError},

		// Retryable statuses: no per-attempt diagnostic (Req 7.2).
		{name: "429 too many requests", outcome: SendOutcome{Status: 429}, want: outcomeRetry, wantDiag: diagNone},
		{name: "500 lower 5xx boundary", outcome: SendOutcome{Status: 500}, want: outcomeRetry, wantDiag: diagNone},
		{name: "599 upper 5xx boundary", outcome: SendOutcome{Status: 599}, want: outcomeRetry, wantDiag: diagNone},

		// Above every real status: still Req 7.15, never a retry.
		{name: "600 above 5xx", outcome: SendOutcome{Status: 600}, want: outcomeDrop, wantDiag: diagErrorStatus, wantLevel: slog.LevelError},

		// No response at all (Req 7.3): both failure categories retry.
		{
			name:     "transport failure retries",
			outcome:  SendOutcome{Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
			want:     outcomeRetry,
			wantDiag: diagNone,
		},
		{
			name:     "context deadline timeout retries",
			outcome:  SendOutcome{Err: fmt.Errorf("betterstack ingest POST timed out: %w", context.DeadlineExceeded)},
			want:     outcomeRetry,
			wantDiag: diagNone,
		},
		{
			name:     "net timeout retries",
			outcome:  SendOutcome{Err: fakeTimeoutError{}},
			want:     outcomeRetry,
			wantDiag: diagNone,
		},

		// The zero SendOutcome: an empty batch that opened no connection
		// (Req 5.12). It must not be read as a transport failure.
		{name: "empty batch is not a failure", outcome: SendOutcome{}, want: outcomeNoRequest, wantDiag: diagNone},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tc.outcome)
			if got.outcome != tc.want {
				t.Errorf("classify(status=%d, err=%v).outcome = %v, want %v",
					tc.outcome.Status, tc.outcome.Err, got.outcome, tc.want)
			}
			if got.diag != tc.wantDiag {
				t.Errorf("classify(status=%d, err=%v).diag = %d, want %d",
					tc.outcome.Status, tc.outcome.Err, got.diag, tc.wantDiag)
			}
			if tc.wantDiag != diagNone && got.diag.level() != tc.wantLevel {
				t.Errorf("classify(status=%d).diag.level() = %v, want %v",
					tc.outcome.Status, got.diag.level(), tc.wantLevel)
			}
		})
	}
}

// TestClassifyCounterEffects checks the counter half of the table: exactly one of
// the delivered and dropped counters advances for a terminal outcome, neither
// advances for a retryable one or for the empty-batch no-op, and every drop this
// file can produce is attributed to reasonNonRetryable (Req 7.1, 7.10, 7.11,
// 7.14, 7.15).
//
// The "neither" case for a retry is the one worth stating explicitly: a retryable
// attempt has not reached a terminal state, and counting its events as dropped
// here would double-count them against the Req 11.4 accounting identity once the
// retry succeeded.
func TestClassifyCounterEffects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status        int
		err           error
		wantDelivered bool
		wantDropped   bool
		wantRetry     bool
	}{
		{status: 202, wantDelivered: true},
		{status: 200, wantDelivered: true},
		{status: 402, wantDropped: true},
		{status: 403, wantDropped: true},
		{status: 406, wantDropped: true},
		{status: 413, wantDropped: true},
		{status: 404, wantDropped: true},
		{status: 429, wantRetry: true},
		{status: 503, wantRetry: true},
		{err: errors.New("dial tcp: connection refused"), wantRetry: true},
		{}, // empty batch: no counter moves at all
	}

	for _, tc := range tests {
		got := classify(SendOutcome{Status: tc.status, Err: tc.err})

		if got.creditsDelivered() != tc.wantDelivered {
			t.Errorf("classify(status=%d, err=%v).creditsDelivered() = %t, want %t",
				tc.status, tc.err, got.creditsDelivered(), tc.wantDelivered)
		}
		if got.creditsDropped() != tc.wantDropped {
			t.Errorf("classify(status=%d, err=%v).creditsDropped() = %t, want %t",
				tc.status, tc.err, got.creditsDropped(), tc.wantDropped)
		}
		if got.retriesBatch() != tc.wantRetry {
			t.Errorf("classify(status=%d, err=%v).retriesBatch() = %t, want %t",
				tc.status, tc.err, got.retriesBatch(), tc.wantRetry)
		}
		if got.creditsDelivered() && got.creditsDropped() {
			t.Errorf("classify(status=%d, err=%v) credits both delivered and dropped", tc.status, tc.err)
		}
		if got.creditsDropped() && got.dropReason() != reasonNonRetryable {
			t.Errorf("classify(status=%d, err=%v).dropReason() = %d, want reasonNonRetryable (%d)",
				tc.status, tc.err, got.dropReason(), reasonNonRetryable)
		}
	}
}

// TestClassifyDiagnosticContent checks which diagnostics are required to name the
// response status and the discarded count: the status for Req 7.11, 7.14, and
// 7.15, the count for Req 7.11 alone, and neither for the 402 and 403 ERRORs,
// which Req 7.10 and 7.8 specify by meaning instead.
func TestClassifyDiagnosticContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status          int
		wantNamesStatus bool
		wantNamesCount  bool
	}{
		{status: 200, wantNamesStatus: true},                       // Req 7.14
		{status: 406, wantNamesStatus: true, wantNamesCount: true}, // Req 7.11
		{status: 413, wantNamesStatus: true, wantNamesCount: true}, // Req 7.11
		{status: 404, wantNamesStatus: true},                       // Req 7.15
		{status: 402},                                              // Req 7.10: account/quota wording
		{status: 403},                                              // Req 7.8: token-rejected wording
		{status: 202},                                              // no diagnostic at all
		{status: 500},                                              // no per-attempt diagnostic
	}

	for _, tc := range tests {
		got := classify(SendOutcome{Status: tc.status})
		if got.diag.namesStatus() != tc.wantNamesStatus {
			t.Errorf("status %d: diag.namesStatus() = %t, want %t", tc.status, got.diag.namesStatus(), tc.wantNamesStatus)
		}
		if got.diag.namesCount() != tc.wantNamesCount {
			t.Errorf("status %d: diag.namesCount() = %t, want %t", tc.status, got.diag.namesCount(), tc.wantNamesCount)
		}
	}
}

// TestClassifyIsPure checks that classify has no state: repeated calls with the
// same input return the same verdict, and no call mutates the SendOutcome it was
// handed. Purity is what makes the classifier safe to call from the delivery
// goroutine and what lets Property 11 (task 8.4) treat it as a function.
func TestClassifyIsPure(t *testing.T) {
	t.Parallel()

	in := SendOutcome{Status: 429, RetryAfter: 7 * time.Second, Err: nil}
	first := classify(in)
	for i := 0; i < 3; i++ {
		if got := classify(in); got != first {
			t.Fatalf("classify is not deterministic: call %d returned %+v, first returned %+v", i+2, got, first)
		}
	}
	if in.Status != 429 || in.RetryAfter != 7*time.Second || in.Err != nil {
		t.Errorf("classify mutated its input: %+v", in)
	}
}
