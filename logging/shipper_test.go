package logging

// Tests for the producer-facing half of the Shipper: construction, Start,
// Handler, Snapshot, and enqueue.
//
// Two things are asserted here that no other test file can assert, because they
// are properties of the Shipper rather than of a Log_Event:
//
//  1. **Goroutine arithmetic.** NewShipper starts nothing, Start starts exactly
//     one, and a disabled Shipper starts none (Req 1.11, 5.3). runtime.NumGoroutine
//     is the only way to see this, so the counting helpers below take some care to
//     be stable rather than merely usually right.
//  2. **Queue occupancy and drop attribution.** The Event_Queue holds at most its
//     capacity at every instant, and each refused event increments exactly one
//     per-reason counter while the console keeps writing (Req 6.2, 6.3, 6.5, 8.9,
//     9.11).
//
// Property 7 (queue capacity, task 6.3) and Property 9 (enqueue never blocks on
// the network, task 6.4) generalize the second group over arbitrary bursts. What
// is here is the small set of examples that pin the contract itself.

import (
	"bytes"
	"context"
	"log/slog"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	testShipperToken = "shipper-test-token"
	testShipperURL   = "https://in.example.test/v1/logs"
)

// newTestShipperConfig resolves a Config the way main.go does — through
// ResolveConfig over raw strings — so the test exercises the same defaulting and
// clamping production uses, then redirects the diagnostic writer into a buffer
// (Req 12.8) and the clock to a fake one (Req 12.2).
//
// enabled == false supplies neither token nor URL, which is the empty-token
// startup path of Req 1.4 and 12.3.
func newTestShipperConfig(t *testing.T, enabled bool, queueSize int) (Config, *bytes.Buffer) {
	t.Helper()

	raw := RawConfig{QueueSize: strconv.Itoa(queueSize)}
	if enabled {
		raw.SourceToken = testShipperToken
		raw.IngestURL = testShipperURL
	}

	cfg, _ := ResolveConfig(raw)
	if cfg.Enabled != enabled {
		t.Fatalf("ResolveConfig produced Enabled = %t, want %t", cfg.Enabled, enabled)
	}
	if cfg.QueueSize != queueSize {
		t.Fatalf("ResolveConfig produced QueueSize = %d, want %d", cfg.QueueSize, queueSize)
	}

	diagnostics := new(bytes.Buffer)
	cfg.DiagWriter = diagnostics
	cfg.Clock = newFakeClock()
	return cfg, diagnostics
}

// stableGoroutineCount returns a goroutine count that has stopped moving.
//
// A bare runtime.NumGoroutine() at the top of a test is not a usable baseline:
// goroutines belonging to the previous test — an httptest server shutting down,
// for instance — may still be exiting, so the count drifts downward and an
// assertion against baseline+1 would fail for a reason that has nothing to do
// with the Shipper. Waiting for two consecutive equal samples removes that whole
// class of flake.
func stableGoroutineCount() int {
	previous := runtime.NumGoroutine()
	for i := 0; i < 200; i++ {
		time.Sleep(waitPollStep)
		current := runtime.NumGoroutine()
		if current == previous {
			return current
		}
		previous = current
	}
	return previous
}

// waitForGoroutineDelta fails unless the goroutine count reaches baseline+delta.
func waitForGoroutineDelta(t *testing.T, baseline, delta int) {
	t.Helper()
	want := baseline + delta
	if !waitFor(func() bool { return runtime.NumGoroutine() == want }, 500*time.Millisecond) {
		t.Fatalf("goroutine count = %d, want baseline+%d = %d", runtime.NumGoroutine(), delta, want)
	}
}

// assertNoGoroutineGrowth fails when the goroutine count has risen above
// baseline, having first given a goroutine that was about to start the chance to
// show up. Polling for equality would pass instantly and prove nothing.
func assertNoGoroutineGrowth(t *testing.T, baseline int, context string) {
	t.Helper()
	time.Sleep(10 * waitPollStep)
	if got := runtime.NumGoroutine(); got > baseline {
		t.Errorf("goroutine count = %d after %s, want at most the baseline %d", got, context, baseline)
	}
}

// stopDeliveryGoroutine releases the delivery goroutine and waits for it to
// exit, so one test cannot leak a goroutine into the next one's baseline.
//
// It closes closeDone directly because Close is task 8.8 and has not landed.
// That is exactly the edge the delivery goroutine already selects on, so this
// exercises the real shutdown path rather than a test-only one.
func stopDeliveryGoroutine(t *testing.T, s *Shipper, baseline int) {
	t.Helper()
	close(s.closeDone)
	waitForGoroutineDelta(t, baseline, 0)
}

// testEvent converts one ERROR record, which is what Handle hands to enqueue.
func testEvent(t *testing.T, message string) *Event {
	t.Helper()
	return convertForTest(t, newTestRecord(slog.LevelError, message))
}

// emitErrors sends n ERROR records through logger, which is the only way a
// production caller ever reaches enqueue.
func emitErrors(logger *slog.Logger, n int, message string) {
	for i := 0; i < n; i++ {
		logger.Error(message, slog.Int("i", i))
	}
}

// consoleLines counts the newline-terminated JSON objects the Console_Handler
// wrote.
func consoleLines(buf *bytes.Buffer) int {
	return bytes.Count(buf.Bytes(), []byte("\n"))
}

func TestNewShipperStartsNothing(t *testing.T) {
	// Req 1.11, 5.3, 12.1: construction wires collaborators and nothing else —
	// no goroutine, no connection, no environment read.
	baseline := stableGoroutineCount()

	cfg, _ := newTestShipperConfig(t, true, 7)
	s := NewShipper(cfg)

	assertNoGoroutineGrowth(t, baseline, "NewShipper")

	if got := cap(s.queue); got != 7 {
		t.Errorf("cap(queue) = %d, want cfg.QueueSize = 7", got)
	}
	if got := len(s.queue); got != 0 {
		t.Errorf("len(queue) = %d, want 0", got)
	}
	if got := s.state.Load(); got != stateEnabled {
		t.Errorf("state = %d, want stateEnabled (%d)", got, stateEnabled)
	}
	if s.closed.Load() {
		t.Error("closed = true on a fresh Shipper, want false")
	}
	if s.client == nil || s.redactor == nil || s.counters == nil || s.diag == nil {
		t.Errorf("collaborators left nil: client=%v redactor=%v counters=%v diag=%v",
			s.client != nil, s.redactor != nil, s.counters != nil, s.diag != nil)
	}
	if s.clock == nil || s.jitter == nil {
		t.Errorf("clock/jitter left nil: clock=%v jitter=%v", s.clock != nil, s.jitter != nil)
	}
	if s.closeDone == nil || s.flushReq == nil {
		t.Errorf("lifecycle channels left nil: closeDone=%v flushReq=%v", s.closeDone != nil, s.flushReq != nil)
	}
	if got := s.Snapshot(); got != (Snapshot{}) {
		t.Errorf("Snapshot() = %+v on a fresh Shipper, want the zero Snapshot", got)
	}
}

func TestNewShipperClampsANonPositiveQueueSize(t *testing.T) {
	// Reachable only from a hand-assembled Config, and make(chan, -1) panics, so
	// the clamp is what keeps a construction slip from taking the process down.
	s := NewShipper(Config{Enabled: true, QueueSize: 0, DiagWriter: new(bytes.Buffer)})

	if got := cap(s.queue); got != defaultQueueSize {
		t.Errorf("cap(queue) = %d for QueueSize 0, want the default %d", got, defaultQueueSize)
	}
}

func TestStartStartsExactlyOneDeliveryGoroutine(t *testing.T) {
	// Req 5.3: exactly one goroutine transmits, which is also what makes
	// Req 5.15's "no second transmission while one is in progress" enforceable
	// without a lock.
	baseline := stableGoroutineCount()

	cfg, _ := newTestShipperConfig(t, true, 4)
	s := NewShipper(cfg)

	s.Start()
	waitForGoroutineDelta(t, baseline, 1)

	// Idempotent: a second consumer of the queue would mean two goroutines could
	// transmit at once.
	s.Start()
	s.Start()
	time.Sleep(10 * waitPollStep)
	if got, want := runtime.NumGoroutine(), baseline+1; got != want {
		t.Errorf("goroutine count = %d after three Start calls, want %d", got, want)
	}

	stopDeliveryGoroutine(t, s, baseline)
}

func TestStartOnADisabledShipperStartsNoGoroutine(t *testing.T) {
	// Req 1.11 and 12.3: an empty token means zero background goroutines and
	// zero outbound connections for the lifetime of the process. No test server
	// is involved, so nothing here can connect either.
	baseline := stableGoroutineCount()

	cfg, diagnostics := newTestShipperConfig(t, false, 4)
	s := NewShipper(cfg)

	if got := s.state.Load(); got != stateDisabled {
		t.Fatalf("state = %d for a disabled Config, want stateDisabled (%d)", got, stateDisabled)
	}

	s.Start()
	s.Start()
	assertNoGoroutineGrowth(t, baseline, "Start on a disabled Shipper")

	if diagnostics.Len() != 0 {
		// Startup diagnostics are emitted by EmitStartupDiagnostics (task 6.2),
		// not by construction or by Start.
		t.Errorf("Start wrote diagnostics on a disabled Shipper: %s", diagnostics)
	}
}

func TestDisabledShipperSnapshotStaysZeroAndDoesNotPanic(t *testing.T) {
	// Req 11.10 and 11.11: a Shipper disabled at startup reports zero for every
	// counter, the queue depth, and the in-flight count in *every* snapshot —
	// including after records at or above the threshold have been emitted.
	cfg, _ := newTestShipperConfig(t, false, 4)
	s := NewShipper(cfg)

	if got := s.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("Snapshot() = %+v before any record, want the zero Snapshot", got)
	}

	handler := s.Handler()
	if handler == nil {
		t.Fatal("Handler() = nil for a disabled Shipper: a nil *Handler stored in a slog.Handler survives the fan-out nil filter and panics")
	}
	// Req 2.9, 2.10: the level gate still answers the threshold question.
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled(ERROR) = false on a disabled Shipper's handler, want true")
	}
	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(INFO) = true at an ERROR threshold, want false")
	}

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), handler))
	emitErrors(logger, 5, "disabled shipper record")

	if got := s.Snapshot(); got != (Snapshot{}) {
		t.Errorf("Snapshot() = %+v after 5 ERROR records, want the zero Snapshot (Req 11.11)", got)
	}
	if got := len(s.queue); got != 0 {
		t.Errorf("len(queue) = %d, want 0: a disabled Shipper enqueues nothing", got)
	}
	// Req 1.10: the console is unaffected by the Shipper being disabled.
	if got := consoleLines(console); got != 5 {
		t.Errorf("console wrote %d records, want 5", got)
	}
}

func TestEnqueueDropsWithQueueFullAtCapacityWhileTheConsoleKeepsWriting(t *testing.T) {
	// Req 6.2, 6.3, and 6.5 together: occupancy stops at the capacity, every
	// refused event is one reasonQueueFull drop, and the console still writes the
	// record for each one.
	cfg, _ := newTestShipperConfig(t, true, 2)
	s := NewShipper(cfg) // deliberately not started: nothing drains the queue

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler()))
	emitErrors(logger, 5, "queue full record")

	if got, want := len(s.queue), cap(s.queue); got != want {
		t.Errorf("len(queue) = %d, want the capacity %d", got, want)
	}

	snap := s.Snapshot()
	if snap.Enqueued != 5 {
		t.Errorf("Enqueued = %d, want 5 admissions", snap.Enqueued)
	}
	if snap.Dropped != 3 {
		t.Errorf("Dropped = %d, want 3 (5 records, capacity 2)", snap.Dropped)
	}
	if got := snap.DroppedByReason[reasonQueueFull]; got != 3 {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want 3", got)
	}
	if got := snap.QueueDepth; got != 2 {
		t.Errorf("QueueDepth = %d, want 2", got)
	}
	if snap.Delivered != 0 || snap.InFlight != 0 {
		t.Errorf("Delivered = %d and InFlight = %d, want 0 and 0: nothing was started", snap.Delivered, snap.InFlight)
	}
	assertCounterIdentities(t, "after a burst past capacity", snap)

	if got := consoleLines(console); got != 5 {
		t.Errorf("console wrote %d records, want 5: a dropped event must not silence stdout (Req 6.5)", got)
	}

	// Req 6.9: once a slot frees up, the next enqueue succeeds and records no
	// drop.
	<-s.queue
	s.enqueue(testEvent(t, "after a slot freed"))
	if got, want := len(s.queue), cap(s.queue); got != want {
		t.Errorf("len(queue) = %d after refilling the freed slot, want %d", got, want)
	}
	if got := s.Snapshot().Dropped; got != 3 {
		t.Errorf("Dropped = %d after a successful enqueue into a freed slot, want 3", got)
	}
}

func TestEnqueueIsWaitFreeAtCapacity(t *testing.T) {
	// Req 5.2, 5.13, 6.3: the select's default arm makes the call wait-free, so a
	// full queue costs tens of nanoseconds rather than blocking the caller. The
	// bound is loose on purpose — this asserts "does not block", not a latency
	// figure; Property 9 (task 6.4) covers independence from server latency.
	cfg, _ := newTestShipperConfig(t, true, 1)
	s := NewShipper(cfg)

	ev := testEvent(t, "wait free")
	const attempts = 20000

	start := time.Now()
	for i := 0; i < attempts; i++ {
		s.enqueue(ev)
	}
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("%d enqueues at capacity took %s, want well under 50ms (1 ms per call is the requirement)", attempts, elapsed)
	}
	if got, want := s.Snapshot().Dropped, uint64(attempts-1); got != want {
		t.Errorf("Dropped = %d, want %d (capacity 1)", got, want)
	}
}

func TestQueueOccupancyNeverExceedsCapacityUnderConcurrentEnqueue(t *testing.T) {
	// Req 6.2: at most the configured capacity at every instant, including while
	// enqueues from multiple goroutines race. Run under -race this also covers
	// Req 11.12's data-race requirement for the producer path.
	const (
		capacity      = 8
		producers     = 8
		perProducer   = 250
		totalAttempts = producers * perProducer
	)

	cfg, _ := newTestShipperConfig(t, true, capacity)
	s := NewShipper(cfg)
	ev := testEvent(t, "concurrent burst")

	overflow := make(chan int, producers)
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				s.enqueue(ev)
				// An observation point between every send. len on a channel is
				// racy as an equality claim but sound as an upper-bound claim:
				// it can never report more elements than the buffer holds.
				if got := len(s.queue); got > capacity {
					select {
					case overflow <- got:
					default:
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	close(overflow)

	for got := range overflow {
		t.Errorf("observed len(queue) = %d, want at most the capacity %d", got, capacity)
	}
	if got := len(s.queue); got != capacity {
		t.Errorf("len(queue) = %d after the burst, want the capacity %d", got, capacity)
	}

	snap := s.Snapshot()
	if got, want := snap.Dropped, uint64(totalAttempts-capacity); got != want {
		t.Errorf("Dropped = %d, want %d (%d attempts, capacity %d)", got, want, totalAttempts, capacity)
	}
	if got := snap.DroppedByReason[reasonQueueFull]; got != snap.Dropped {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want every drop = %d", got, snap.Dropped)
	}
}

func TestEnqueueAfterCloseDropsWithReasonAfterClose(t *testing.T) {
	// Req 9.11: an enqueue after the close operation returned discards the event,
	// counts the drop, and returns immediately, while the console keeps writing.
	// closed is set directly because Close is task 8.8; the flag it stores is the
	// only thing the enqueue path reads.
	cfg, _ := newTestShipperConfig(t, true, 4)
	s := NewShipper(cfg)
	s.closed.Store(true)

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler()))
	emitErrors(logger, 3, "after close record")

	if got := len(s.queue); got != 0 {
		t.Errorf("len(queue) = %d after close, want 0", got)
	}

	snap := s.Snapshot()
	if got := snap.DroppedByReason[reasonAfterClose]; got != 3 {
		t.Errorf("DroppedByReason[reasonAfterClose] = %d, want 3", got)
	}
	if snap.Dropped != 3 || snap.Enqueued != 3 {
		t.Errorf("Dropped = %d and Enqueued = %d, want 3 and 3", snap.Dropped, snap.Enqueued)
	}
	if got := snap.DroppedByReason[reasonQueueFull]; got != 0 {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want 0: the queue had free capacity", got)
	}
	assertCounterIdentities(t, "after enqueue past close", snap)

	if got := consoleLines(console); got != 3 {
		t.Errorf("console wrote %d records, want 3 (Req 9.11)", got)
	}
}

func TestEnqueueAfterMidLifeDisableDropsWithoutEnqueueing(t *testing.T) {
	// Req 8.9: a Shipper that disables during operation — HTTP 403 (Req 7.8) or
	// five recovered delivery panics (Req 8.4) — discards subsequent events with
	// zero enqueues, while the console keeps writing.
	//
	// The drop is attributed to reasonNonRetryable rather than to a sixth
	// reasonDisabled, because Req 11.5 fixes the reason set at five and requires
	// their sum to equal the total. Both routes into this state are non-retryable
	// terminal conditions, and reasonAfterClose keeps meaning only what Req 9.11
	// says it means.
	cfg, _ := newTestShipperConfig(t, true, 4)
	s := NewShipper(cfg)

	// Bound while still enabled, as main.go's startup wiring does.
	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler()))

	s.state.Store(stateDisabled)
	emitErrors(logger, 2, "post disable record")

	if got := len(s.queue); got != 0 {
		t.Errorf("len(queue) = %d after mid-life disable, want 0", got)
	}

	snap := s.Snapshot()
	if got := snap.DroppedByReason[reasonNonRetryable]; got != 2 {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want 2", got)
	}
	if snap.Dropped != 2 {
		t.Errorf("Dropped = %d, want 2", snap.Dropped)
	}
	assertCounterIdentities(t, "after enqueue while disabled", snap)

	if got := consoleLines(console); got != 2 {
		t.Errorf("console wrote %d records, want 2 (Req 8.9)", got)
	}
}

func TestSuspendedShipperKeepsAcceptingEvents(t *testing.T) {
	// Req 7.12: enqueue stays open for the whole suspension window and for every
	// backoff wait. Only transmission stops, and that decision belongs to the
	// delivery goroutine.
	cfg, _ := newTestShipperConfig(t, true, 4)
	s := NewShipper(cfg)
	s.state.Store(stateSuspended)

	s.enqueue(testEvent(t, "during suspension"))

	if got := len(s.queue); got != 1 {
		t.Errorf("len(queue) = %d while suspended, want 1", got)
	}
	if got := s.Snapshot().Dropped; got != 0 {
		t.Errorf("Dropped = %d while suspended, want 0", got)
	}
}

func TestHandlerUsesTheConfiguredThreshold(t *testing.T) {
	// Req 2.1: the handler is thresholded at cfg.MinLevel, so BETTERSTACK_MIN_LEVEL
	// governs what is forwarded and nothing else does.
	cfg, _ := newTestShipperConfig(t, true, 4)
	cfg.MinLevel = slog.LevelWarn
	s := NewShipper(cfg)

	handler := s.Handler()
	if !handler.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Enabled(WARN) = false at a WARN threshold, want true")
	}
	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(INFO) = true at a WARN threshold, want false")
	}

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), handler))
	logger.Warn("warn record")
	logger.Info("info record")

	events := queuedEvents(s)
	if len(events) != 1 || events[0].Level != "WARN" {
		t.Fatalf("queued %d events %v, want exactly the WARN record", len(events), events)
	}
	// Req 2.4: a below-threshold record produces no admission at all.
	if got := s.Snapshot().Enqueued; got != 1 {
		t.Errorf("Enqueued = %d, want 1: the INFO record must not be admitted", got)
	}
	if got := consoleLines(console); got != 2 {
		t.Errorf("console wrote %d records, want 2: the console keeps every level (Req 2.5)", got)
	}
}

func TestStartedShipperDrainsTheQueue(t *testing.T) {
	// The delivery goroutine consumes the queue and accounts for what it took as
	// in-flight (Req 11.3), so the Req 11.4 identity holds while an event is out
	// of the channel and not yet at a terminal state.
	//
	// Three records at the default BatchSize of 20, on a fake clock that is never
	// advanced, is deliberately the case that reaches neither delivery trigger:
	// they sit in the pending batch, so in-flight is exactly where they belong.
	// The batching triggers themselves are covered in delivery_test.go.
	baseline := stableGoroutineCount()

	cfg, _ := newTestShipperConfig(t, true, 4)
	s := NewShipper(cfg)
	s.Start()
	waitForGoroutineDelta(t, baseline, 1)

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler()))
	emitErrors(logger, 3, "drained record")

	if !waitFor(func() bool { return s.Snapshot().InFlight == 3 }, 500*time.Millisecond) {
		t.Errorf("InFlight = %d, want 3 events taken off the queue", s.Snapshot().InFlight)
	}

	snap := s.Snapshot()
	if snap.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0: the queue had free capacity throughout", snap.Dropped)
	}
	assertCounterIdentities(t, "after the delivery goroutine drained the queue", snap)

	stopDeliveryGoroutine(t, s, baseline)
}
