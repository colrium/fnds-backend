package logging

// Tests for the periodic health summary (Req 11.6, 11.7, 11.8, 11.10).
//
// Tasks 8.12, 8.13, and 8.14 append property tests and further example tests to
// this file, so every helper declared here is prefixed `health` and every
// constant `healthTest`, leaving the shared names in diag_test.go, shipper_test.go,
// and handler_test.go untouched.
//
// The four claims, and how each is made observable:
//
//	Req 11.6  advance a fake clock by 5 minutes after a counter moved -> one INFO
//	          line carrying every counter, all five per-reason drops, the queue
//	          depth, and the in-flight count, and all of it from one snapshot
//	Req 11.7  advance again with nothing having happened -> zero lines, and the
//	          test server records zero requests
//	Req 11.8  the Shipper's own Handler is behind slog.Default() while summaries
//	          are written -> zero admissions and zero queued events
//	Req 11.10 a disabled Shipper -> zero lines, ever

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// healthTestToken is the Source_Token no summary may ever name (Req 7.13,
	// 10.4). It is distinctive so a substring search over the whole diagnostic
	// buffer is conclusive.
	healthTestToken = "health-test-source-token-QQRST"
	// healthTestWait bounds every wait for a summary that must arrive. It is a
	// polling ceiling, not a sleep: waitFor returns as soon as the condition
	// holds, so a passing test costs a few milliseconds (Req 12.2).
	healthTestWait = 2 * time.Second
	// healthTestSettle is how long a summary that must *not* arrive is given to
	// arrive before the absence is believed. An absence cannot be waited for, so
	// this one is paid in full every time it is used, which is why it is 25 polls
	// rather than the 2000 of healthTestWait — the same figure suspend_disable_test.go
	// uses for the same reason, and far inside the 5-second budget of Req 12.2.
	healthTestSettle = 25 * waitPollStep
)

// healthDiag is the diagnostic writer every test below uses.
//
// It holds a mutex around a bytes.Buffer because the delivery goroutine writes
// summaries to it while the test goroutine polls it, and a bare bytes.Buffer
// there is a data race the -race detector reports (Req 11.12). suspend_disable_test.go
// carries the same type for the same reason; this one is separate so the two
// files stay independent.
type healthDiag struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (d *healthDiag) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.Write(p)
}

func (d *healthDiag) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

func (d *healthDiag) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf.Reset()
}

// empty reports whether nothing at all has been written, which is the literal
// form of "zero Self_Diagnostics" in Req 11.7 and Req 11.10.
func (d *healthDiag) empty() bool { return strings.TrimSpace(d.text()) == "" }

// healthShipper builds and starts a Shipper whose diagnostics land in the
// returned writer and whose time is entirely under the test's control, and waits
// until both of the delivery goroutine's tickers exist.
//
// Waiting for the tickers is what makes the clock advances below deterministic:
// advancing before the health ticker is registered would move time past a
// boundary that no ticker was watching for, and the test would then wait for a
// tick that is never coming.
//
// The Config is assembled by hand rather than through ResolveConfig for the
// reason delivery_test.go gives: Req 1.6 requires https and an httptest.Server is
// http. The Shipper is released through closeDone, the edge run itself selects
// on, because Close belongs to task 8.8.
func healthShipper(t *testing.T, url string) (*Shipper, *healthDiag, *fakeClock) {
	t.Helper()

	diagnostics := new(healthDiag)
	clock := newFakeClock()
	cfg := Config{
		Enabled:     true,
		IngestURL:   url,
		SourceToken: healthTestToken,
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   4,
		FlushEvery:  defaultFlushSeconds * time.Second,
		QueueSize:   8,
		DiagWriter:  diagnostics,
		Clock:       clock,
	}

	s := NewShipper(cfg)
	s.Start()
	t.Cleanup(func() { close(s.closeDone) })

	// Two tickers: the flush ticker and the 5-minute health ticker.
	if !clock.WaitTickers(2, healthTestWait) {
		t.Fatalf("the delivery goroutine registered %d tickers within %s, want the flush ticker and the health ticker",
			clock.TickerCount(), healthTestWait)
	}
	return s, diagnostics, clock
}

// healthServer returns a test server that accepts every batch and counts the
// requests it received, so "zero HTTP requests" is measured rather than assumed
// (Req 11.7).
func healthServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	return server, func() int { return int(requests.Load()) }
}

// healthSummaries returns the summary diagnostics written so far, identified by
// message so an unrelated diagnostic — a delivery WARN, say — cannot be mistaken
// for one.
func healthSummaries(t *testing.T, diagnostics *healthDiag) []map[string]any {
	t.Helper()

	var summaries []map[string]any
	for i, line := range strings.Split(strings.TrimSpace(diagnostics.text()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("diagnostic line %d is not JSON: %v (%q)", i, err, line)
		}
		if record["msg"] == healthSummaryMessage {
			summaries = append(summaries, record)
		}
	}
	return summaries
}

// healthAwaitSummaries blocks until at least n summaries have been written, and
// reports whether they were. The delivery goroutine writes them, so a test that
// advanced the clock has to wait for that goroutine rather than read the writer
// immediately.
func healthAwaitSummaries(t *testing.T, diagnostics *healthDiag, n int) bool {
	t.Helper()
	return waitFor(func() bool { return len(healthSummaries(t, diagnostics)) >= n }, healthTestWait)
}

// healthSummaryArrives gives the delivery goroutine healthTestSettle to write an
// nth summary and reports whether it did.
//
// It is the negative form of healthAwaitSummaries and exists so a silence claim
// is not made against a buffer read the instant after the clock moved, which
// would pass even for a Shipper that summarises every interval. Its budget is
// short because it is spent in full on every passing assertion.
func healthSummaryArrives(t *testing.T, diagnostics *healthDiag, n int) bool {
	t.Helper()
	return waitFor(func() bool { return len(healthSummaries(t, diagnostics)) >= n }, healthTestSettle)
}

// healthUint reads a counter out of a summary. json.Unmarshal decodes every JSON
// number into a float64, and every value in a summary is a small integer, so the
// conversion is exact.
func healthUint(t *testing.T, summary map[string]any, key string) uint64 {
	t.Helper()
	value, ok := summary[key].(float64)
	if !ok {
		t.Fatalf("summary field %q = %#v, want a JSON number", key, summary[key])
	}
	return uint64(value)
}

// healthRequiredKeys is every field Req 11.6 requires a summary to carry: the six
// counters, the five per-reason dropped-event counts, the Event_Queue depth, and
// the in-flight count.
func healthRequiredKeys() []string {
	keys := []string{
		"enqueued", "delivered", "dropped",
		"conversion_panics", "retried_batches", "abandoned_batches",
	}
	keys = append(keys, healthDropReasonKeys[:]...)
	return append(keys, "queue_depth", "in_flight")
}

func TestHealthSummaryFiresAfterFiveMinutesWhenACounterMoved(t *testing.T) {
	// Req 11.6: 5 minutes on the injected clock, one INFO Self_Diagnostic, every
	// required field, all of it from one snapshot.
	server, requestCount := healthServer(t)
	s, diagnostics, clock := healthShipper(t, server.URL)

	const emitted = 4 // == BatchSize, so the batch goes out at once (Req 5.5)
	deliveryEmit(deliveryLogger(s), "health", emitted)
	if !waitFor(func() bool { return s.Snapshot().Delivered == emitted }, healthTestWait) {
		t.Fatalf("Delivered = %d, want %d before the interval is advanced", s.Snapshot().Delivered, emitted)
	}

	// Nothing yet: the interval has not elapsed.
	if got := healthSummaries(t, diagnostics); len(got) != 0 {
		t.Fatalf("wrote %d summaries before the first interval elapsed, want 0: %q", len(got), diagnostics.text())
	}

	clock.Advance(healthSummaryInterval)

	if !healthAwaitSummaries(t, diagnostics, 1) {
		t.Fatalf("no summary within %s of the 5-minute boundary, want exactly one (Req 11.6): %q",
			healthTestWait, diagnostics.text())
	}
	summaries := healthSummaries(t, diagnostics)
	if len(summaries) != 1 {
		t.Fatalf("wrote %d summaries for one interval, want exactly 1: %q", len(summaries), diagnostics.text())
	}
	summary := summaries[0]

	// Req 11.6: INFO, and the marker attribute that identifies shipper
	// self-diagnostic output (Req 8.1).
	if got := summary["level"]; got != slog.LevelInfo.String() {
		t.Errorf("level = %v, want %s", got, slog.LevelInfo)
	}
	if summary["logshipper"] != true {
		t.Errorf("summary = %v, want the logshipper marker", summary)
	}

	// Every required field is present.
	for _, key := range healthRequiredKeys() {
		if _, ok := summary[key]; !ok {
			t.Errorf("summary is missing %q, which Req 11.6 requires: %v", key, summary)
		}
	}

	// One snapshot, not several: the numbers in the line satisfy the Req 11.4
	// accounting identity and the Req 11.5 per-reason sum. A summary assembled
	// from field-by-field reads would eventually fail this.
	enqueued := healthUint(t, summary, "enqueued")
	delivered := healthUint(t, summary, "delivered")
	dropped := healthUint(t, summary, "dropped")
	inFlight := healthUint(t, summary, "in_flight")
	depth := healthUint(t, summary, "queue_depth")
	if enqueued != delivered+dropped+inFlight+depth {
		t.Errorf("enqueued %d != delivered %d + dropped %d + in_flight %d + queue_depth %d: the summary was not built from one snapshot (Req 11.4, 11.6)",
			enqueued, delivered, dropped, inFlight, depth)
	}
	var reasonSum uint64
	for _, key := range healthDropReasonKeys {
		reasonSum += healthUint(t, summary, key)
	}
	if reasonSum != dropped {
		t.Errorf("per-reason drops sum to %d, want the dropped total %d (Req 11.5)", reasonSum, dropped)
	}

	// And the values are the ones the Shipper actually holds.
	if enqueued != emitted || delivered != emitted {
		t.Errorf("enqueued = %d, delivered = %d, want %d for both", enqueued, delivered, emitted)
	}
	if got := requestCount(); got != 1 {
		t.Errorf("the server received %d requests, want the single delivery batch: a summary transmits nothing", got)
	}
	if strings.Contains(diagnostics.text(), healthTestToken) {
		t.Errorf("a summary named the Source_Token (Req 7.13, 10.4): %q", diagnostics.text())
	}
}

func TestHealthSummaryStaysSilentWhenNoCounterMoved(t *testing.T) {
	// Req 11.7: an interval in which zero counters increased produces zero
	// Self_Diagnostics and zero HTTP requests. The whole test never emits a
	// record, so no counter can have moved.
	server, requestCount := healthServer(t)
	_, diagnostics, clock := healthShipper(t, server.URL)

	for interval := 1; interval <= 3; interval++ {
		clock.Advance(healthSummaryInterval)
		// The tick has to be given a chance to be processed before the buffer is
		// judged empty, so the wait is for a summary that must not arrive rather
		// than an immediate read.
		if healthSummaryArrives(t, diagnostics, 1) {
			t.Fatalf("wrote a summary at interval %d with no counter having moved, want silence (Req 11.7): %q",
				interval, diagnostics.text())
		}
	}
	if !diagnostics.empty() {
		t.Errorf("wrote %q across three idle intervals, want nothing at all (Req 11.7)", diagnostics.text())
	}
	if got := requestCount(); got != 0 {
		t.Errorf("the server received %d requests across three idle intervals, want 0 (Req 11.7)", got)
	}
}

func TestHealthSummaryReportsAgainOnlyAfterSomethingMoves(t *testing.T) {
	// Req 11.6 and 11.7 together across four intervals: report, silence, silence,
	// report. This is the claim a summary driven by the tick alone would fail.
	server, requestCount := healthServer(t)
	s, diagnostics, clock := healthShipper(t, server.URL)
	logger := deliveryLogger(s)

	deliveryEmit(logger, "first", 4)
	if !waitFor(func() bool { return s.Snapshot().Delivered == 4 }, healthTestWait) {
		t.Fatalf("Delivered = %d, want 4", s.Snapshot().Delivered)
	}

	clock.Advance(healthSummaryInterval)
	if !healthAwaitSummaries(t, diagnostics, 1) {
		t.Fatalf("no summary for the first active interval: %q", diagnostics.text())
	}

	// Two idle intervals: still exactly one summary in total.
	for interval := 2; interval <= 3; interval++ {
		clock.Advance(healthSummaryInterval)
		if healthSummaryArrives(t, diagnostics, 2) {
			t.Fatalf("wrote a second summary at idle interval %d, want silence until a counter moves (Req 11.7): %q",
				interval, diagnostics.text())
		}
	}

	// Something moves again: exactly one more summary, carrying the cumulative
	// counters rather than per-interval deltas.
	deliveryEmit(logger, "second", 4)
	if !waitFor(func() bool { return s.Snapshot().Delivered == 8 }, healthTestWait) {
		t.Fatalf("Delivered = %d, want 8", s.Snapshot().Delivered)
	}
	clock.Advance(healthSummaryInterval)
	if !healthAwaitSummaries(t, diagnostics, 2) {
		t.Fatalf("no summary after a counter moved again, want one (Req 11.6): %q", diagnostics.text())
	}

	summaries := healthSummaries(t, diagnostics)
	if len(summaries) != 2 {
		t.Fatalf("wrote %d summaries across four intervals, want exactly 2: %q", len(summaries), diagnostics.text())
	}
	if got := healthUint(t, summaries[0], "delivered"); got != 4 {
		t.Errorf("first summary delivered = %d, want 4", got)
	}
	if got := healthUint(t, summaries[1], "delivered"); got != 8 {
		t.Errorf("second summary delivered = %d, want the cumulative 8", got)
	}
	if got := requestCount(); got != 2 {
		t.Errorf("the server received %d requests, want the 2 delivery batches: the idle intervals transmitted nothing (Req 11.7)", got)
	}
}

func TestHealthSummaryProducesNoLogEvents(t *testing.T) {
	// Req 11.8 with the feedback loop wired: the Shipper's own Handler is behind
	// slog.Default(), so if a summary were forwarded it would be admitted and
	// queued. Zero admissions and zero queued events is the claim.
	//
	// The Shipper is deliberately not started — nothing drains the queue, so an
	// admitted summary would still be sitting in it — and the evaluation is driven
	// directly, which is the only way to write a summary with no delivery
	// goroutine running.
	diagnostics := new(healthDiag)
	s := NewShipper(Config{
		Enabled:     true,
		IngestURL:   "https://in.health.example.test/v1/logs?source_token=SECRET",
		SourceToken: healthTestToken,
		MinLevel:    slog.LevelError,
		BatchSize:   4,
		FlushEvery:  defaultFlushSeconds * time.Second,
		QueueSize:   8,
		DiagWriter:  diagnostics,
		Clock:       newFakeClock(),
	})

	console := new(bytes.Buffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler())))
	t.Cleanup(func() { slog.SetDefault(previous) })

	monitor := &healthMonitor{}
	// A counter has to have moved for a summary to be written at all, and it is
	// moved without slog: enqueue is what a record would have reached.
	s.counters.addAdmitted(1)
	s.counters.addDropped(reasonQueueFull, 1)

	s.maybeHealthSummary(monitor)
	// A second evaluation with nothing new: still no events, and no second line.
	s.maybeHealthSummary(monitor)

	if got := healthSummaries(t, diagnostics); len(got) != 1 {
		t.Fatalf("wrote %d summaries for one moved counter and two evaluations, want exactly 1: %q",
			len(got), diagnostics.text())
	}
	if got := queuedEvents(s); len(got) != 0 {
		t.Errorf("the Event_Queue holds %d events after a summary was written, want 0 (Req 11.8)", len(got))
	}
	snap := s.Snapshot()
	if snap.Enqueued != 1 {
		t.Errorf("Enqueued = %d, want the 1 admission this test recorded: a summary must not be admitted (Req 11.8)", snap.Enqueued)
	}
	if console.Len() != 0 {
		t.Errorf("the console handler wrote %q, want nothing: a summary goes to the diagnostic writer, not through slog.Default() (Req 8.1)", console)
	}
	if strings.Contains(diagnostics.text(), healthTestToken) || strings.Contains(diagnostics.text(), "SECRET") {
		t.Errorf("a summary named the Source_Token or the endpoint query string: %q", diagnostics.text())
	}
}

func TestHealthSummaryWritesNothingWhileDisabled(t *testing.T) {
	// Req 11.10: zero periodic summary Self_Diagnostics while disabled — both for
	// a Shipper disabled at startup, which runs no delivery goroutine at all
	// (Req 1.11), and for one that disables mid-life, whose health ticker goes on
	// ticking.
	t.Run("disabled at startup", func(t *testing.T) {
		diagnostics := new(healthDiag)
		clock := newFakeClock()
		s := NewShipper(Config{
			DiagWriter: diagnostics,
			Clock:      clock,
			MinLevel:   slog.LevelError,
			QueueSize:  4,
			BatchSize:  4,
			FlushEvery: defaultFlushSeconds * time.Second,
		})
		s.Start()

		if got := clock.TickerCount(); got != 0 {
			t.Errorf("a disabled Shipper registered %d tickers, want 0 (Req 1.11, 11.10)", got)
		}
		// Even driven directly, with counters that moved, it writes nothing.
		s.counters.addAdmitted(3)
		s.maybeHealthSummary(&healthMonitor{})
		if !diagnostics.empty() {
			t.Errorf("a Shipper disabled at startup wrote %q, want no summary (Req 11.10)", diagnostics.text())
		}
	})

	t.Run("disabled after startup", func(t *testing.T) {
		diagnostics := new(healthDiag)
		s := NewShipper(Config{
			Enabled:    true,
			IngestURL:  "https://in.health.example.test/v1/logs",
			MinLevel:   slog.LevelError,
			QueueSize:  4,
			BatchSize:  4,
			FlushEvery: defaultFlushSeconds * time.Second,
			DiagWriter: diagnostics,
			Clock:      newFakeClock(),
		})
		monitor := &healthMonitor{}

		s.counters.addAdmitted(1)
		s.maybeHealthSummary(monitor)
		if got := healthSummaries(t, diagnostics); len(got) != 1 {
			t.Fatalf("wrote %d summaries while enabled, want 1 so the disabled claim below is meaningful", len(got))
		}
		diagnostics.reset()

		// The mid-life transition of Req 7.8 and 8.4.
		s.state.Store(stateDisabled)
		s.counters.addDropped(reasonNonRetryable, 5) // counters can still move (Req 8.9)
		for evaluation := 0; evaluation < 3; evaluation++ {
			s.maybeHealthSummary(monitor)
		}
		if !diagnostics.empty() {
			t.Errorf("wrote %q across three evaluations while disabled, want nothing (Req 11.10)", diagnostics.text())
		}
	})
}

func TestHealthCountersIncreasedIgnoresGaugesOnly(t *testing.T) {
	// The change test itself, which is what Req 11.7 keys silence on: the eleven
	// cumulative counters count, and the two gauges of Req 11.3 do not. The case
	// in the last subtest is real — an event enqueued in one interval and picked
	// up by the delivery goroutine in the next moves both gauges and no counter —
	// and Req 11.7 requires silence for it.
	base := Snapshot{
		Enqueued: 10, Delivered: 6, Dropped: 3,
		ConversionPanics: 1, RetriedBatches: 2, AbandonedBatches: 1,
		DroppedByReason: [numDropReasons]uint64{1, 1, 1, 0, 0},
		QueueDepth:      1, InFlight: 0,
	}

	if healthCountersIncreased(base, base) {
		t.Error("healthCountersIncreased = true for an unchanged snapshot, want false (Req 11.7)")
	}

	for name, mutate := range map[string]func(*Snapshot){
		"enqueued":          func(s *Snapshot) { s.Enqueued++ },
		"delivered":         func(s *Snapshot) { s.Delivered++ },
		"dropped":           func(s *Snapshot) { s.Dropped++ },
		"conversion panics": func(s *Snapshot) { s.ConversionPanics++ },
		"retried batches":   func(s *Snapshot) { s.RetriedBatches++ },
		"abandoned batches": func(s *Snapshot) { s.AbandonedBatches++ },
		"after close drops": func(s *Snapshot) { s.DroppedByReason[reasonAfterClose]++ },
	} {
		moved := base
		mutate(&moved)
		if !healthCountersIncreased(base, moved) {
			t.Errorf("healthCountersIncreased = false after %s increased, want true (Req 11.6)", name)
		}
	}

	// Gauges only: the event left the queue and entered flight.
	gaugesOnly := base
	gaugesOnly.QueueDepth, gaugesOnly.InFlight = 0, 1
	if healthCountersIncreased(base, gaugesOnly) {
		t.Error("healthCountersIncreased = true for a gauge-only change, want false: QueueDepth and InFlight are not counters (Req 11.3, 11.7)")
	}

	// A counter that went backwards, which only a bug could produce, is not an
	// increase.
	backwards := base
	backwards.Delivered--
	if healthCountersIncreased(base, backwards) {
		t.Error("healthCountersIncreased = true for a decreased counter, want false")
	}
}

func TestHealthMonitorIsUsableWithoutATicker(t *testing.T) {
	// A Shipper composed literally has no clock, and run's health arm must still
	// be a legal select arm: tick() returns nil, a receive from which blocks
	// forever, so the arm exists and never fires. stop() on the same monitor is a
	// no-op rather than a nil dereference.
	var monitor *healthMonitor
	if monitor.tick() != nil {
		t.Error("(*healthMonitor)(nil).tick() != nil, want a nil channel that can never fire")
	}
	monitor.stop() // must not panic

	empty := &healthMonitor{}
	if empty.tick() != nil {
		t.Error("a monitor with no ticker returned a non-nil channel")
	}
	empty.stop()

	// And the summary path tolerates both nil receivers, because Req 11.10 says
	// the health path may never take the process down.
	var s *Shipper
	s.maybeHealthSummary(empty)
	NewShipper(Config{DiagWriter: io.Discard}).maybeHealthSummary(nil)
}

func TestHealthSummaryUnderConcurrentProducers(t *testing.T) {
	// Req 11.12 for this path: the summary reads the counters while producers are
	// incrementing them, and every line it writes still satisfies the Req 11.4
	// identity because it comes from one Snapshot. Under -race this also covers
	// the monitor being private to the delivery goroutine — the evaluation here is
	// driven from one goroutine only, as run drives it.
	diagnostics := new(healthDiag)
	s := NewShipper(Config{
		Enabled:    true,
		IngestURL:  "https://in.health.example.test/v1/logs",
		MinLevel:   slog.LevelError,
		QueueSize:  16,
		BatchSize:  4,
		FlushEvery: defaultFlushSeconds * time.Second,
		DiagWriter: diagnostics,
		Clock:      newFakeClock(),
	})
	// The records reach the Shipper the way production's do — through the fan-out
	// handler, so Handle counts the admission before enqueue disposes of it. That
	// matters for the identity asserted below: driving s.enqueue directly would
	// record drops against admissions nobody counted, and the identity would fail
	// for the harness rather than for the code.
	logger := deliveryLogger(s)

	const (
		producers = 4
		perGo     = 200
	)
	var producing sync.WaitGroup
	for p := 0; p < producers; p++ {
		producing.Add(1)
		go func() {
			defer producing.Done()
			emitErrors(logger, perGo, "concurrent health")
		}()
	}

	// One evaluator, because run has one: the monitor is private to the goroutine
	// that ticks it, and a second evaluator would be a data race by construction
	// rather than a test of anything.
	var evaluating sync.WaitGroup
	stop := make(chan struct{})
	monitor := &healthMonitor{}
	evaluating.Add(1)
	go func() {
		defer evaluating.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.maybeHealthSummary(monitor)
			}
		}
	}()

	producing.Wait()
	close(stop)
	evaluating.Wait()

	for i, summary := range healthSummaries(t, diagnostics) {
		enqueued := healthUint(t, summary, "enqueued")
		sum := healthUint(t, summary, "delivered") + healthUint(t, summary, "dropped") +
			healthUint(t, summary, "in_flight") + healthUint(t, summary, "queue_depth")
		if enqueued != sum {
			t.Fatalf("summary %d: enqueued %d != delivered + dropped + in_flight + queue_depth = %d (%v)",
				i, enqueued, sum, summary)
		}
	}
}

func TestHealthStartupDiagnosticContainsEffectiveConfigurationAndNoSecrets(t *testing.T) {
	const (
		token       = "health-startup-source-token"
		host        = "ingest.health-startup.example.test:8443"
		querySecret = "health-startup-query-secret"
	)
	diagnostics := new(healthDiag)
	s := NewShipper(Config{
		Enabled:     true,
		IngestURL:   "https://user:password@" + host + "/v1/logs?source_token=" + querySecret,
		SourceToken: token,
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelWarn,
		BatchSize:   37,
		FlushEvery:  11 * time.Second,
		QueueSize:   123,
		DiagWriter:  diagnostics,
		Clock:       newFakeClock(),
	})

	s.EmitStartupDiagnostics(nil)

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(diagnostics.text())), &record); err != nil {
		t.Fatalf("startup diagnostic is not JSON: %v (%q)", err, diagnostics.text())
	}
	if record["level"] != slog.LevelInfo.String() {
		t.Errorf("startup diagnostic level = %v, want INFO", record["level"])
	}
	for key, want := range map[string]any{
		"min_level":      slog.LevelWarn.String(),
		"batch_size":     float64(37),
		"flush_seconds":  float64(11),
		"queue_capacity": float64(123),
		"ingest_host":    host,
	} {
		if got := record[key]; got != want {
			t.Errorf("startup diagnostic %s = %v, want %v", key, got, want)
		}
	}
	for _, secret := range []string{token, querySecret, "user:password", "/v1/logs"} {
		if strings.Contains(diagnostics.text(), secret) {
			t.Errorf("startup diagnostic contains excluded endpoint/token data %q: %q", secret, diagnostics.text())
		}
	}
}

func TestHealthPeriodicSummaryAfterFiveMinutesDependsOnCounterChanges(t *testing.T) {
	server, requestCount := healthServer(t)
	s, diagnostics, clock := healthShipper(t, server.URL)

	clock.Advance(healthSummaryInterval)
	if healthSummaryArrives(t, diagnostics, 1) {
		t.Fatalf("idle +5m evaluation wrote a summary, want zero: %q", diagnostics.text())
	}
	if got := requestCount(); got != 0 {
		t.Fatalf("idle +5m evaluation made %d requests, want zero", got)
	}

	// Move an admission and its terminal drop together so the snapshot remains
	// balanced without putting an event on the queue or opening a connection.
	s.counters.addAdmitted(1)
	s.counters.addDropped(reasonQueueFull, 1)
	clock.Advance(healthSummaryInterval)
	if !healthAwaitSummaries(t, diagnostics, 1) {
		t.Fatalf("active +5m evaluation wrote no summary: %q", diagnostics.text())
	}
	if got := len(healthSummaries(t, diagnostics)); got != 1 {
		t.Errorf("active +5m evaluation wrote %d summaries, want exactly one", got)
	}
	if got := requestCount(); got != 0 {
		t.Errorf("periodic summary made %d requests, want zero", got)
	}
}
