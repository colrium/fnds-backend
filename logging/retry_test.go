package logging

// Unit tests for the retry policy of logging/retry.go: the doubling schedule and
// its 30 s cap, the Retry-After clamp, the jitter bounds, the 5-attempt ceiling,
// and the abandonment effects (Req 7.2 through 7.7, 7.12).
//
// Property P16 (task 8.5) generalises the delay assertions over arbitrary
// attempt and Retry-After inputs and reads the schedule back from the fake
// clock's recorded After durations. These tests pin the exact table instead, so a
// change to the schedule fails with a concrete expected-versus-got pair rather
// than as a shrunk counterexample. Names here are deliberately distinct from the
// property's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	retryTestToken = "bs-retry-secret-token"
	retryTestURL   = "https://in.example.com/v1?source_token=bs-retry-secret-token"
)

// newRetryTestShipper returns a Shipper whose clock and jitter are both
// injected, together with the fake clock and the buffer its diagnostics land in.
//
// The Config is composed literally rather than through ResolveConfig because
// these tests need an exact jitter value, which no raw configuration can express.
func newRetryTestShipper(t *testing.T, jitter Jitter) (*Shipper, *fakeClock, *strings.Builder) {
	t.Helper()

	clock := newFakeClock()
	diagnostics := new(strings.Builder)
	s := NewShipper(Config{
		Enabled:     true,
		MinLevel:    slog.LevelError,
		IngestURL:   retryTestURL,
		SourceToken: retryTestToken,
		QueueSize:   8,
		BatchSize:   4,
		FlushEvery:  time.Second,
		DiagWriter:  diagnostics,
		Clock:       clock,
		Jitter:      jitter,
	})
	return s, clock, diagnostics
}

// TestRetryDelayFollowsDoublingSchedule pins the attempt-to-base table of
// Req 7.4: 1, 2, 4, 8, 16 s, then the 30 s cap. Jitter is fixed at 0 so the
// delay is exactly the base.
func TestRetryDelayFollowsDoublingSchedule(t *testing.T) {
	s, _, _ := newRetryTestShipper(t, FixedJitter(0))

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 1 * time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 5, want: 16 * time.Second},
	}
	for _, tc := range tests {
		if got := s.retryDelay(tc.attempt, 0); got != tc.want {
			t.Errorf("retryDelay(%d, 0) = %v, want %v", tc.attempt, got, tc.want)
		}
		if got := baseRetryDelay(tc.attempt, 0); got != tc.want {
			t.Errorf("baseRetryDelay(%d, 0) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestRetryDelayCapsBaseAtThirtySeconds covers every attempt beyond the
// enumerated schedule, including values far past the 5-attempt ceiling and the
// ones that would overflow `1 << (attempt-1)` if the guard were missing
// (Req 7.4).
func TestRetryDelayCapsBaseAtThirtySeconds(t *testing.T) {
	s, _, _ := newRetryTestShipper(t, FixedJitter(0))

	for _, attempt := range []int{6, 7, 10, 40, 64, 1000, math.MaxInt32, math.MaxInt} {
		if got := s.retryDelay(attempt, 0); got != maxBaseRetryDelay {
			t.Errorf("retryDelay(%d, 0) = %v, want %v", attempt, got, maxBaseRetryDelay)
		}
	}

	// A non-positive attempt can only come from a caller that computed a delay
	// before transmitting. It must still yield a positive, bounded delay.
	for _, attempt := range []int{0, -1, math.MinInt} {
		got := s.retryDelay(attempt, 0)
		if got != baseRetryDelayUnit {
			t.Errorf("retryDelay(%d, 0) = %v, want %v", attempt, got, baseRetryDelayUnit)
		}
	}
}

// TestRetryDelayRetryAfterReplacesBaseClamped covers the Req 7.4 clamp: a
// positive Retry-After replaces the computed base entirely and is confined to
// 1-300 s at both ends.
func TestRetryDelayRetryAfterReplacesBaseClamped(t *testing.T) {
	s, _, _ := newRetryTestShipper(t, FixedJitter(0))

	tests := []struct {
		name       string
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{name: "below the floor", attempt: 3, retryAfter: 500 * time.Millisecond, want: 1 * time.Second},
		{name: "at the floor", attempt: 3, retryAfter: 1 * time.Second, want: 1 * time.Second},
		{name: "inside the range replaces a larger base", attempt: 5, retryAfter: 7 * time.Second, want: 7 * time.Second},
		{name: "inside the range replaces a smaller base", attempt: 1, retryAfter: 90 * time.Second, want: 90 * time.Second},
		{name: "at the ceiling", attempt: 2, retryAfter: 300 * time.Second, want: 300 * time.Second},
		{name: "above the ceiling", attempt: 2, retryAfter: 600 * time.Second, want: 300 * time.Second},
		{name: "the ingest client's arithmetic cap", attempt: 4, retryAfter: maxRetryAfterSeconds * time.Second, want: 300 * time.Second},
		{name: "zero falls back to the schedule", attempt: 4, retryAfter: 0, want: 8 * time.Second},
		{name: "negative falls back to the schedule", attempt: 2, retryAfter: -5 * time.Second, want: 2 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.retryDelay(tc.attempt, tc.retryAfter); got != tc.want {
				t.Errorf("retryDelay(%d, %v) = %v, want %v", tc.attempt, tc.retryAfter, got, tc.want)
			}
		})
	}
}

// TestRetryAfterForStatusHonoursOnly429 asserts the Req 7.4 restriction that
// Retry-After is honoured for HTTP 429 alone: a 5xx carrying the header still
// follows the doubling schedule.
func TestRetryAfterForStatusHonoursOnly429(t *testing.T) {
	const header = 45 * time.Second

	if got := retryAfterForStatus(http.StatusTooManyRequests, header); got != header {
		t.Errorf("retryAfterForStatus(429, %v) = %v, want %v", header, got, header)
	}
	for _, status := range []int{0, 200, 202, 402, 403, 406, 413, 500, 503, 599} {
		if got := retryAfterForStatus(status, header); got != 0 {
			t.Errorf("retryAfterForStatus(%d, %v) = %v, want 0", status, header, got)
		}
	}

	// End to end through retryDelay: the same header shapes a 429's delay and
	// is ignored for a 503.
	s, _, _ := newRetryTestShipper(t, FixedJitter(0))
	if got := s.retryDelay(2, retryAfterForStatus(http.StatusTooManyRequests, header)); got != header {
		t.Errorf("429 delay = %v, want %v", got, header)
	}
	if got := s.retryDelay(2, retryAfterForStatus(http.StatusServiceUnavailable, header)); got != 2*time.Second {
		t.Errorf("503 delay = %v, want %v", got, 2*time.Second)
	}
}

// TestRetryDelayJitterStaysWithinTwentyPercent asserts the Req 7.5 bound: a
// fraction of 0 gives exactly the base, a fraction just below 1 gives just under
// 1.2 x base, and every intermediate fraction lands inside [base, 1.2 x base].
func TestRetryDelayJitterStaysWithinTwentyPercent(t *testing.T) {
	bases := []struct {
		attempt    int
		retryAfter time.Duration
		base       time.Duration
	}{
		{attempt: 1, base: 1 * time.Second},
		{attempt: 3, base: 4 * time.Second},
		{attempt: 5, base: 16 * time.Second},
		{attempt: 9, base: 30 * time.Second},
		{attempt: 2, retryAfter: 300 * time.Second, base: 300 * time.Second},
	}

	for _, b := range bases {
		zero, _, _ := newRetryTestShipper(t, FixedJitter(0))
		if got := zero.retryDelay(b.attempt, b.retryAfter); got != b.base {
			t.Errorf("FixedJitter(0) delay for base %v = %v, want exactly the base", b.base, got)
		}

		// The largest fraction a Jitter may report is math.Nextafter(1, 0), so
		// the delay approaches but never reaches 1.2 x base.
		top, _, _ := newRetryTestShipper(t, FixedJitter(1))
		ceiling := b.base + time.Duration(maxJitterShare*float64(b.base))
		got := top.retryDelay(b.attempt, b.retryAfter)
		if got >= ceiling {
			t.Errorf("near-1 jitter delay for base %v = %v, want < %v", b.base, got, ceiling)
		}
		if floor := b.base + time.Duration(0.199*float64(b.base)); got <= floor {
			t.Errorf("near-1 jitter delay for base %v = %v, want > %v", b.base, got, floor)
		}

		// Every fraction in between, including a randomised source, stays in
		// range.
		for i, jitter := range []Jitter{FixedJitter(0.5), FixedJitter(0.999), RandJitter(7)} {
			s, _, _ := newRetryTestShipper(t, jitter)
			for draw := 0; draw < 32; draw++ {
				got := s.retryDelay(b.attempt, b.retryAfter)
				if got < b.base || got >= ceiling {
					t.Fatalf("jitter %d draw %d: delay for base %v = %v, want in [%v, %v)", i, draw, b.base, got, b.base, ceiling)
				}
			}
		}
	}
}

// TestRetryDelayNeverExceedsAbsoluteMaximum asserts the Req 7.5 ceiling of
// 360 s (300 x 1.2), including for a Jitter that violates its own [0, 1)
// contract.
func TestRetryDelayNeverExceedsAbsoluteMaximum(t *testing.T) {
	attempts := []int{-1, 1, 5, 6, 100, math.MaxInt}
	retryAfters := []time.Duration{0, -1, 1, 42 * time.Second, 300 * time.Second, 10000 * time.Second, time.Duration(math.MaxInt64)}
	jitters := []Jitter{
		FixedJitter(0),
		FixedJitter(1),
		RandJitter(3),
		hostileJitter{f: 1},
		hostileJitter{f: 5000},
		hostileJitter{f: math.Inf(1)},
		hostileJitter{f: math.NaN()},
		hostileJitter{f: -1},
	}

	for _, jitter := range jitters {
		s, _, _ := newRetryTestShipper(t, jitter)
		for _, attempt := range attempts {
			for _, retryAfter := range retryAfters {
				got := s.retryDelay(attempt, retryAfter)
				if got > maxRetryDelay {
					t.Errorf("retryDelay(%d, %v) with jitter %v = %v, want <= %v", attempt, retryAfter, jitter, got, maxRetryDelay)
				}
				if got < baseRetryDelayUnit {
					t.Errorf("retryDelay(%d, %v) with jitter %v = %v, want at least %v", attempt, retryAfter, jitter, got, baseRetryDelayUnit)
				}
				if base := baseRetryDelay(attempt, retryAfter); got < base {
					t.Errorf("retryDelay(%d, %v) with jitter %v = %v, want at least the base %v", attempt, retryAfter, jitter, got, base)
				}
			}
		}
	}
}

// hostileJitter reports a fraction outside the [0, 1) contract, which
// FixedJitter would clamp. It exists to prove the sanitising in jitterFraction,
// and with it the Req 7.5 bound, holds for a Jitter supplied from outside this
// package.
type hostileJitter struct{ f float64 }

func (j hostileJitter) Fraction() float64 { return j.f }

func (j hostileJitter) String() string { return fmt.Sprintf("hostileJitter(%v)", j.f) }

// TestJitterFractionSanitisesOutOfRangeValues checks the sanitising directly, so
// a failure points at the guard rather than at a delay several steps downstream.
func TestJitterFractionSanitisesOutOfRangeValues(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{in: 0, want: 0},
		{in: 0.25, want: 0.25},
		{in: maxJitterFraction, want: maxJitterFraction},
		{in: 1, want: maxJitterFraction},
		{in: 12.5, want: maxJitterFraction},
		{in: math.Inf(1), want: maxJitterFraction},
		{in: -0.5, want: 0},
		{in: math.Inf(-1), want: 0},
		{in: math.NaN(), want: 0},
	}
	for _, tc := range tests {
		s, _, _ := newRetryTestShipper(t, hostileJitter{f: tc.in})
		if got := s.jitterFraction(); got != tc.want {
			t.Errorf("jitterFraction() for %v = %v, want %v", tc.in, got, tc.want)
		}
	}

	// A Shipper composed literally has no jitter source and must still produce a
	// usable delay rather than panicking.
	var bare Shipper
	if got := bare.jitterFraction(); got != 0 {
		t.Errorf("jitterFraction() with a nil jitter = %v, want 0", got)
	}
	if got := bare.retryDelay(2, 0); got != 2*time.Second {
		t.Errorf("retryDelay with a nil jitter = %v, want %v", got, 2*time.Second)
	}
}

// TestBatchAttemptCountsAtMostFiveAttempts asserts Req 7.6: the count is 1 after
// the first transmission, rises by exactly 1 per retransmission, and never
// exceeds 5 — so one batch appears in at most 5 requests.
func TestBatchAttemptCountsAtMostFiveAttempts(t *testing.T) {
	var a batchAttempt
	if a.attempts != 0 {
		t.Fatalf("a fresh batchAttempt reports %d attempts, want 0", a.attempts)
	}
	if a.exhausted() {
		t.Fatal("a fresh batchAttempt reports exhausted")
	}

	for i := 1; i <= maxTransmitAttempts; i++ {
		a.record(SendOutcome{Status: http.StatusServiceUnavailable})
		if a.attempts != i {
			t.Fatalf("after %d transmissions attempts = %d, want %d", i, a.attempts, i)
		}
		wantExhausted := i == maxTransmitAttempts
		if a.exhausted() != wantExhausted {
			t.Fatalf("after %d transmissions exhausted() = %t, want %t", i, a.exhausted(), wantExhausted)
		}
	}

	// A caller bug that keeps transmitting must not push the count past 5.
	for i := 0; i < 4; i++ {
		a.record(SendOutcome{Status: http.StatusInternalServerError})
	}
	if a.attempts != maxTransmitAttempts {
		t.Errorf("attempts = %d after extra transmissions, want %d", a.attempts, maxTransmitAttempts)
	}

	// The last outcome is always the most recent one, which is what the
	// abandonment diagnostic reports (Req 7.7).
	transport := errors.New("dial tcp: connection refused")
	a.record(SendOutcome{Err: transport})
	if a.status != 0 || !errors.Is(a.err, transport) {
		t.Errorf("last outcome = (status %d, err %v), want (0, %v)", a.status, a.err, transport)
	}
}

// TestBatchAttemptVerdict asserts the retry-versus-abandon half of Req 7.2, 7.3,
// and 7.7: retry while fewer than 5 attempts have happened, abandon once the
// fifth ended retryably, and never retry a non-retryable outcome.
func TestBatchAttemptVerdict(t *testing.T) {
	var a batchAttempt
	for i := 1; i < maxTransmitAttempts; i++ {
		a.record(SendOutcome{Status: http.StatusTooManyRequests})
		if got := a.verdict(true); got != retryVerdictRetry {
			t.Errorf("verdict after attempt %d = %v, want retry", i, got)
		}
		if got := a.verdict(false); got != retryVerdictAbandon {
			t.Errorf("non-retryable verdict after attempt %d = %v, want abandon", i, got)
		}
	}

	a.record(SendOutcome{Status: http.StatusTooManyRequests})
	if got := a.verdict(true); got != retryVerdictAbandon {
		t.Errorf("verdict after attempt %d = %v, want abandon", maxTransmitAttempts, got)
	}
}

// TestAbandonBatchCountsDropsAndWritesOneError asserts every effect Req 7.7
// enumerates for a batch that exhausted its attempts on a retryable status.
func TestAbandonBatchCountsDropsAndWritesOneError(t *testing.T) {
	s, _, diagnostics := newRetryTestShipper(t, FixedJitter(0))

	batch := []*Event{{Message: "first"}, {Message: "second"}, {Message: "third"}}
	s.counters.addAdmitted(uint64(len(batch)))
	s.counters.addInFlight(int64(len(batch)))

	a := batchAttempt{}
	for i := 0; i < maxTransmitAttempts; i++ {
		a.record(SendOutcome{Status: http.StatusServiceUnavailable})
	}
	s.abandonBatch(batch, a)

	snap := s.Snapshot()
	if snap.Dropped != 3 || snap.DroppedByReason[reasonAbandonedBatch] != 3 {
		t.Errorf("dropped = %d (abandoned reason %d), want 3 and 3", snap.Dropped, snap.DroppedByReason[reasonAbandonedBatch])
	}
	if snap.AbandonedBatches != 1 {
		t.Errorf("abandoned batches = %d, want 1", snap.AbandonedBatches)
	}
	if snap.InFlight != 0 {
		t.Errorf("in flight = %d, want 0", snap.InFlight)
	}
	if snap.Enqueued != snap.Delivered+snap.Dropped+snap.InFlight+snap.QueueDepth {
		t.Errorf("accounting identity broken: %+v", snap)
	}

	records := decodeRetryDiagnostics(t, diagnostics.String())
	if len(records) != 1 {
		t.Fatalf("wrote %d diagnostics, want exactly 1: %q", len(records), diagnostics.String())
	}
	rec := records[0]
	if rec["level"] != "ERROR" {
		t.Errorf("diagnostic level = %v, want ERROR", rec["level"])
	}
	if rec["logshipper"] != true {
		t.Errorf("diagnostic is missing the logshipper marker: %v", rec)
	}
	if got := rec["discarded_events"]; got != float64(len(batch)) {
		t.Errorf("discarded_events = %v, want %d", got, len(batch))
	}
	if got := rec["status"]; got != float64(http.StatusServiceUnavailable) {
		t.Errorf("status = %v, want %d", got, http.StatusServiceUnavailable)
	}
	if got := rec["attempts"]; got != float64(maxTransmitAttempts) {
		t.Errorf("attempts = %v, want %d", got, maxTransmitAttempts)
	}
	if strings.Contains(diagnostics.String(), retryTestToken) {
		t.Errorf("the diagnostic leaked the source token: %q", diagnostics.String())
	}
}

// TestAbandonBatchReportsTransportCategory covers the other half of Req 7.7's
// diagnostic content: when the last attempt received no response, the ERROR
// names the transport failure category instead of a status.
func TestAbandonBatchReportsTransportCategory(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: fmt.Errorf("betterstack ingest POST timed out: %w", context.DeadlineExceeded), want: "timeout"},
		{name: "transport", err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused"), want: "transport"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, diagnostics := newRetryTestShipper(t, FixedJitter(0))
			batch := []*Event{{Message: "only"}}
			s.counters.addAdmitted(1)
			s.counters.addInFlight(1)

			a := batchAttempt{}
			for i := 0; i < maxTransmitAttempts; i++ {
				a.record(SendOutcome{Err: tc.err})
			}
			s.abandonBatch(batch, a)

			records := decodeRetryDiagnostics(t, diagnostics.String())
			if len(records) != 1 {
				t.Fatalf("wrote %d diagnostics, want exactly 1: %q", len(records), diagnostics.String())
			}
			if got := records[0]["failure"]; got != tc.want {
				t.Errorf("failure = %v, want %q", got, tc.want)
			}
			if _, ok := records[0]["status"]; ok {
				t.Errorf("a transport failure reported a status: %v", records[0])
			}
			if got := s.Snapshot(); got.AbandonedBatches != 1 || got.InFlight != 0 {
				t.Errorf("counters after abandonment = %+v, want 1 abandoned batch and 0 in flight", got)
			}
		})
	}
}

// TestRetryTransportCategoryNamesEveryCase pins the category strings, including
// the caller-bug case of no error at all.
func TestRetryTransportCategoryNamesEveryCase(t *testing.T) {
	if got := retryTransportCategory(nil); got != "unknown" {
		t.Errorf("retryTransportCategory(nil) = %q, want %q", got, "unknown")
	}
	if got := retryTransportCategory(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("retryTransportCategory(DeadlineExceeded) = %q, want %q", got, "timeout")
	}
	if got := retryTransportCategory(errors.New("no such host")); got != "transport" {
		t.Errorf("retryTransportCategory(dns failure) = %q, want %q", got, "transport")
	}
}

// TestRetryTimerWaitsOnTheInjectedClock asserts Req 12.2 for the backoff wait:
// the delay is taken from the injected clock, is recorded there, and fires only
// once the clock has been advanced to it.
func TestRetryTimerWaitsOnTheInjectedClock(t *testing.T) {
	s, clock, _ := newRetryTestShipper(t, FixedJitter(0))

	timer, delay := s.retryTimer(3, 0)
	if delay != 4*time.Second {
		t.Fatalf("retryTimer reported delay %v, want %v", delay, 4*time.Second)
	}
	if got := clock.AfterDurations(); len(got) != 1 || got[0] != delay {
		t.Fatalf("clock recorded %v, want exactly [%v]", got, delay)
	}

	clock.Advance(delay - time.Nanosecond)
	select {
	case <-timer:
		t.Fatal("the backoff fired before its delay elapsed")
	default:
	}

	clock.Advance(time.Nanosecond)
	select {
	case <-timer:
	default:
		t.Fatal("the backoff did not fire once its delay elapsed")
	}

	// waitRetry consumes the whole delay on the same clock. It blocks, so the
	// clock is advanced from a second goroutine.
	waited := make(chan time.Duration, 1)
	go func() { waited <- s.waitRetry(1, 0) }()
	if !clock.WaitAfters(2, 50*time.Millisecond) {
		t.Fatal("waitRetry never called After on the injected clock")
	}
	clock.Advance(baseRetryDelayUnit)
	select {
	case got := <-waited:
		if got != baseRetryDelayUnit {
			t.Errorf("waitRetry reported %v, want %v", got, baseRetryDelayUnit)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("waitRetry did not return after the clock reached its delay")
	}

	// Every wait in this test came from the fake clock, so the whole schedule is
	// readable back from it (property P16's mechanism).
	if got := clock.AfterDurations(); len(got) != 2 {
		t.Errorf("clock recorded %d After calls, want 2: %v", len(got), got)
	}
}

// TestEnqueueStaysOpenDuringBackoff asserts Req 7.12: while a backoff wait is
// outstanding, the Event_Queue keeps accepting Log_Events up to its capacity and
// discards the ones that arrive when it is full.
func TestEnqueueStaysOpenDuringBackoff(t *testing.T) {
	s, clock, _ := newRetryTestShipper(t, FixedJitter(0))

	timer, delay := s.retryTimer(5, 0) // a 16 s backoff, un-elapsed
	capacity := cap(s.queue)

	for i := 0; i < capacity; i++ {
		s.counters.addAdmitted(1)
		s.enqueue(&Event{Message: fmt.Sprintf("during backoff %d", i)})
	}
	if snap := s.Snapshot(); snap.QueueDepth != uint64(capacity) || snap.Dropped != 0 {
		t.Fatalf("during backoff: queue depth %d and %d drops, want %d and 0", snap.QueueDepth, snap.Dropped, capacity)
	}

	// At capacity the overflow is dropped and counted, exactly as at idle.
	s.counters.addAdmitted(1)
	s.enqueue(&Event{Message: "overflow"})
	if snap := s.Snapshot(); snap.DroppedByReason[reasonQueueFull] != 1 {
		t.Errorf("overflow during backoff: queue-full drops = %d, want 1", snap.DroppedByReason[reasonQueueFull])
	}

	// The backoff is still outstanding: accepting events neither fired nor
	// shortened it.
	select {
	case <-timer:
		t.Fatal("the backoff fired while events were being enqueued")
	default:
	}
	clock.Advance(delay)
	select {
	case <-timer:
	default:
		t.Fatal("the backoff did not fire once its delay elapsed")
	}
}

// decodeRetryDiagnostics parses the JSON lines a diagnostic writer collected.
func decodeRetryDiagnostics(t *testing.T, out string) []map[string]any {
	t.Helper()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("diagnostic line %q is not JSON: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}
