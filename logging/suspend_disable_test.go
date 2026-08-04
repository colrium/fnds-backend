package logging

// Tests for the delivery outcome pipeline of logging/shipper.go's transmit: the
// classification of every terminal status, the retry loop and its backoff, the
// 402 suspension window, and the permanent disable HTTP 403 causes.
//
// Why these tests need a server rather than a direct call to transmit: the
// requirements they cover are all statements about *HTTP requests* — how many
// were issued, when, and none at all after a 403 (Req 7.6, 7.9, 7.10). A unit
// test of classify can tell you what 403 means; only a real delivery goroutine
// driving a real client at a recording endpoint can tell you that nothing is
// transmitted afterwards.
//
// Three disciplines hold throughout:
//
//   - **No sleeping for time to pass.** Every backoff delay and the whole
//     15-minute suspension window are driven by advancing the injected fakeClock
//     (Req 12.2). The only real sleeps are waitFor's 1 ms polls, which
//     synchronise with the delivery goroutine, and one short settle in the
//     places where the assertion is "nothing happened" and polling for zero
//     would prove nothing.
//   - **No network.** Every endpoint is loopback, checked rather than assumed
//     (Req 12.3).
//   - **The accounting identity is asserted at every checkpoint** (Req 11.4),
//     because the paths under test are exactly the ones that move events between
//     in-flight, delivered, and dropped.
//
// Every identifier here is prefixed `sd`/`SD` (suspend/disable) so this file
// cannot collide with logging/delivery_test.go, which tasks 6.6 and 6.7 are
// appending to.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sdWaitTimeout bounds every wait for the delivery goroutine to reach a state.
// It is only ever waited out by a failing test.
const sdWaitTimeout = 2 * time.Second

// sdSettle is how long a "nothing happened" assertion gives a request that was
// about to be issued to arrive. Polling for zero would succeed instantly and
// prove nothing, so these assertions pay a fixed, small wait instead.
const sdSettle = 25 * waitPollStep

// sdFlushEvery is the flush interval of every Shipper here. It is long enough
// that no test can be flushed by accident: every transmission is caused either
// by the batch-size trigger or by an explicit clock advance.
const sdFlushEvery = 60 * time.Second

const (
	sdBatchSize = 2
	sdQueueSize = 64
	// sdToken is the Source_Token no diagnostic may ever contain (Req 7.13,
	// 10.4). It is deliberately a distinctive string so a substring search for
	// it is meaningful.
	sdToken = "bs-suspend-disable-secret-token"
)

// ---------------------------------------------------------------------------
// A scripted ingest endpoint
// ---------------------------------------------------------------------------

// sdServer answers each request with the next status of a script, repeating the
// last entry once the script runs out, and counts every request it received.
//
// The optional gate holds the *first* response open. That is what lets a test
// put Log_Events into the Event_Queue while the delivery goroutine is parked
// inside Send: the 403 drain of Req 7.8 is only meaningful when the queue is
// non-empty at the moment the 403 lands, and the only way to guarantee that is
// to keep the delivery goroutine out of its select.
type sdServer struct {
	*httptest.Server

	mu         sync.Mutex
	script     []int
	retryAfter string
	count      int
	gate       chan struct{}
}

// newSDServer starts a scripted endpoint and registers its shutdown. The script
// must hold at least one status; the last one answers every further request.
func newSDServer(t *testing.T, script ...int) *sdServer {
	t.Helper()
	if len(script) == 0 {
		t.Fatal("newSDServer needs at least one status")
	}

	server := &sdServer{script: script}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		server.mu.Lock()
		server.count++
		index := server.count - 1
		status := server.script[len(server.script)-1]
		if index < len(server.script) {
			status = server.script[index]
		}
		gate, retryAfter := server.gate, server.retryAfter
		server.mu.Unlock()

		// The request is counted *before* the gate wait, so waitRequests(1)
		// reports true while this handler is still holding the response open.
		if gate != nil && index == 0 {
			<-gate
		}
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)

	assertLoopbackEndpoint(t, server.URL) // Req 12.3
	return server
}

// withRetryAfter makes every response carry the given Retry-After header value.
func (s *sdServer) withRetryAfter(value string) *sdServer {
	s.mu.Lock()
	s.retryAfter = value
	s.mu.Unlock()
	return s
}

// withGate holds the first response open until releaseGate is called. The gate is
// released on cleanup as well, so a failing test cannot leave the handler and the
// delivery goroutine parked.
func (s *sdServer) withGate(t *testing.T) *sdServer {
	t.Helper()
	s.mu.Lock()
	s.gate = make(chan struct{})
	s.mu.Unlock()
	t.Cleanup(s.releaseGate)
	return s
}

// releaseGate lets the first response complete. It is idempotent.
func (s *sdServer) releaseGate() {
	s.mu.Lock()
	gate := s.gate
	s.gate = nil
	s.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

// requests reports how many requests have arrived, including one still being
// held open by the gate.
func (s *sdServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// waitRequests blocks until at least n requests have arrived.
func (s *sdServer) waitRequests(n int) bool {
	return waitFor(func() bool { return s.requests() >= n }, sdWaitTimeout)
}

// assertRequests fails unless exactly want requests have arrived.
func (s *sdServer) assertRequests(t *testing.T, want int, why string) {
	t.Helper()
	if got := s.requests(); got != want {
		t.Fatalf("%s: the endpoint received %d requests, want exactly %d", why, got, want)
	}
}

// ---------------------------------------------------------------------------
// A diagnostic writer that is safe to read while the shipper writes
// ---------------------------------------------------------------------------

// sdDiag is the Self_Diagnostic writer for these tests. The delivery goroutine
// writes to it while the test goroutine reads it, so it holds a mutex — a bare
// bytes.Buffer would be a data race the -race detector reports (Req 11.12).
type sdDiag struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (d *sdDiag) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.Write(p)
}

func (d *sdDiag) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// lines returns the diagnostic lines written so far.
func (d *sdDiag) lines() []string {
	trimmed := strings.TrimSpace(d.text())
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// waitRecords blocks until at least n diagnostic lines have been written, so an
// assertion about a diagnostic never races the goroutine that writes it.
func (d *sdDiag) waitRecords(n int) bool {
	return waitFor(func() bool { return len(d.lines()) >= n }, sdWaitTimeout)
}

// records decodes every diagnostic line as JSON.
func (d *sdDiag) records(t *testing.T) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, line := range d.lines() {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("diagnostic line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// only returns the single diagnostic written, failing when there is not exactly
// one. "Exactly one" is the requirement in every case this file covers
// (Req 7.7, 7.8, 7.10, 7.11, 7.14, 7.15), so it is asserted rather than assumed.
func (d *sdDiag) only(t *testing.T) map[string]any {
	t.Helper()

	if !d.waitRecords(1) {
		t.Fatalf("no Self_Diagnostic was written within %s", sdWaitTimeout)
	}
	// A second diagnostic would be a requirement violation, so give one the
	// chance to appear rather than reading the first line and moving on.
	time.Sleep(sdSettle)

	records := d.records(t)
	if len(records) != 1 {
		t.Fatalf("wrote %d Self_Diagnostics, want exactly 1: %q", len(records), d.text())
	}
	rec := records[0]
	if rec["logshipper"] != true {
		t.Errorf("the diagnostic is missing the logshipper marker attribute (Req 8.1): %v", rec)
	}
	if strings.Contains(d.text(), sdToken) {
		t.Errorf("the diagnostic leaked the Source_Token (Req 7.13, 10.4): %q", d.text())
	}
	return rec
}

// assertDiagField fails unless the record carries key with the given numeric
// value. slog's JSON handler renders every number as a JSON number, which
// decodes into float64.
func assertDiagField(t *testing.T, rec map[string]any, key string, want int, req string) {
	t.Helper()
	got, ok := rec[key]
	if !ok {
		t.Errorf("the diagnostic has no %q field, which %s requires: %v", key, req, rec)
		return
	}
	if got != float64(want) {
		t.Errorf("diagnostic %s = %v, want %d (%s)", key, got, want, req)
	}
}

// ---------------------------------------------------------------------------
// Shipper construction
// ---------------------------------------------------------------------------

// sdShipper is a started Shipper together with everything a test needs to drive
// and observe it.
type sdShipper struct {
	*Shipper
	clock  *fakeClock
	diag   *sdDiag
	server *sdServer

	// log is built once, while the Shipper is still enabled, because that is
	// what production does: main.go installs NewFanoutHandler(console,
	// shipper.Handler()) at startup and keeps it for the lifetime of the
	// process. Asking for a fresh Handler after a 403 would hand back the
	// conversion-only handler of Req 11.11 instead, and the mid-life disabling
	// of Req 8.9 — which is what these tests exercise — happens on the enqueue
	// path of an already-bound handler.
	log *slog.Logger

	stopOnce sync.Once
}

// newSDShipper builds a Shipper aimed at server, starts its delivery goroutine,
// and waits until the flush ticker exists — so a test that advances the clock
// cannot advance it before the ticker was registered and then wonder why no tick
// arrived.
//
// The Config is composed literally rather than through ResolveConfig because
// Req 1.6 requires an https scheme and an httptest.Server is http, and because
// these tests need an exact jitter value, which no raw configuration can express.
// Every other field carries the value ResolveConfig would have produced.
func newSDShipper(t *testing.T, server *sdServer) *sdShipper {
	t.Helper()

	clock := newFakeClock()
	diag := &sdDiag{}
	s := NewShipper(Config{
		Enabled:     true,
		IngestURL:   server.URL,
		SourceToken: sdToken,
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   sdBatchSize,
		FlushEvery:  sdFlushEvery,
		QueueSize:   sdQueueSize,
		DiagWriter:  diag,
		Clock:       clock,
		// Zero jitter, so a recorded backoff is exactly the base delay of
		// Req 7.4 and the schedule can be compared against the table.
		Jitter: FixedJitter(0),
	})

	sd := &sdShipper{
		Shipper: s,
		clock:   clock,
		diag:    diag,
		server:  server,
		// Bound through the fan-out, so the enqueued counter is incremented by
		// Handle and the console child keeps writing, exactly as in production.
		log: slog.New(NewFanoutHandler(slog.NewJSONHandler(io.Discard, nil), s.Handler())),
	}
	s.Start()
	// closeDone is the edge run itself selects on; Close is task 8.8.
	t.Cleanup(sd.stop)

	if !clock.WaitTickers(1, sdWaitTimeout) {
		t.Fatalf("the delivery goroutine registered no flush ticker within %s", sdWaitTimeout)
	}
	return sd
}

// stop releases the delivery goroutine. It is idempotent, so a test may end the
// loop explicitly — the close path of Req 7.9 — and still be cleaned up.
func (sd *sdShipper) stop() {
	sd.stopOnce.Do(func() { close(sd.closeDone) })
}

// emit writes n ERROR records through the handler bound at construction, which
// is n admissions and n enqueue attempts.
func (sd *sdShipper) emit(n int) {
	for i := 0; i < n; i++ {
		sd.log.Error("suspend-disable event")
	}
}

// waitSnapshot blocks until the snapshot satisfies cond, and returns the last
// snapshot it observed either way.
func (sd *sdShipper) waitSnapshot(cond func(Snapshot) bool) (Snapshot, bool) {
	ok := waitFor(func() bool { return cond(sd.Snapshot()) }, sdWaitTimeout)
	return sd.Snapshot(), ok
}

// waitState blocks until the delivery state reaches want.
func (sd *sdShipper) waitState(want int32) bool {
	return waitFor(func() bool { return sd.state.Load() == want }, sdWaitTimeout)
}

// assertState fails unless the delivery state is want.
func (sd *sdShipper) assertState(t *testing.T, want int32, why string) {
	t.Helper()
	if got := sd.state.Load(); got != want {
		t.Fatalf("%s: state = %s, want %s", why, sdStateName(got), sdStateName(want))
	}
}

func sdStateName(state int32) string {
	switch state {
	case stateEnabled:
		return "enabled"
	case stateDisabled:
		return "disabled"
	case stateSuspended:
		return "suspended"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// HTTP 402: suspension (Req 7.10, 7.12)
// ---------------------------------------------------------------------------

// TestSD402SuspendsTransmissionAndResumesAtTheFirstFlushBoundary covers Req 7.10
// end to end, plus the half of Req 7.12 that concerns a suspension: the batch
// that drew the 402 is dropped without retransmission, one ERROR about account or
// quota reasons is written, the Event_Queue keeps accepting, zero HTTP requests
// are issued for 15 minutes on the injected clock, and the first flush boundary
// after the window transmits again.
//
// Every duration comes from the fake clock, so the 15 minutes cost no wall-clock
// time (Req 12.2).
func TestSD402SuspendsTransmissionAndResumesAtTheFirstFlushBoundary(t *testing.T) {
	// 402 once, then a healthy endpoint, so the resumption is observable as a
	// delivery rather than only as a state change.
	server := newSDServer(t, http.StatusPaymentRequired, http.StatusAccepted)
	sd := newSDShipper(t, server)

	// The batch-size trigger transmits without any interval elapsing (Req 5.5).
	sd.emit(sdBatchSize)

	if !server.waitRequests(1) {
		t.Fatalf("no Delivery_Batch was transmitted within %s", sdWaitTimeout)
	}
	if !sd.waitState(stateSuspended) {
		t.Fatalf("state = %s after HTTP 402, want suspended (Req 7.10)", sdStateName(sd.state.Load()))
	}

	// The batch is discarded without retransmission and counted as a
	// non-retryable drop (Req 7.10).
	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped == sdBatchSize })
	if !ok {
		t.Fatalf("Dropped = %d after HTTP 402, want %d (%+v)", snap.Dropped, sdBatchSize, snap)
	}
	if got := snap.DroppedByReason[reasonNonRetryable]; got != sdBatchSize {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d (Req 7.10)", got, sdBatchSize)
	}
	if snap.Delivered != 0 || snap.InFlight != 0 {
		t.Errorf("Delivered = %d, InFlight = %d after HTTP 402, want 0 and 0 (%+v)",
			snap.Delivered, snap.InFlight, snap)
	}
	if snap.RetriedBatches != 0 {
		t.Errorf("RetriedBatches = %d, want 0: 402 is not retryable (Req 7.10)", snap.RetriedBatches)
	}
	assertCounterIdentities(t, "after HTTP 402 suspended transmission", snap)
	server.assertRequests(t, 1, "402 must not be retransmitted")

	// Exactly one ERROR, about account or quota reasons, naming no token
	// (Req 7.10, 7.13).
	rec := sd.diag.only(t)
	if rec["level"] != "ERROR" {
		t.Errorf("the 402 diagnostic level = %v, want ERROR (Req 7.10)", rec["level"])
	}
	message, _ := rec["msg"].(string)
	if !strings.Contains(message, "account or quota") {
		t.Errorf("the 402 diagnostic message = %q, want it to state that the batch was rejected for account or quota reasons (Req 7.10)", message)
	}
	assertDiagField(t, rec, "status", http.StatusPaymentRequired, "Req 7.10")
	assertDiagField(t, rec, "discarded_events", sdBatchSize, "Req 7.10")

	// Req 7.12: the Event_Queue keeps accepting during the window. One event is
	// below the batch size, so it is batched and held, and no request is made.
	sd.emit(1)
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.InFlight == 1 })
	if !ok {
		t.Fatalf("InFlight = %d after an event arrived during the suspension, want 1: enqueue must stay open (Req 7.12) (%+v)",
			snap.InFlight, snap)
	}
	if got := snap.DroppedByReason[reasonQueueFull]; got != 0 {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want 0: the queue was far below capacity (Req 7.12)", got)
	}
	if snap.Enqueued != sdBatchSize+1 {
		t.Errorf("Enqueued = %d, want %d: every admission during the suspension is counted", snap.Enqueued, sdBatchSize+1)
	}
	assertCounterIdentities(t, "with an event accepted during the suspension", snap)
	server.assertRequests(t, 1, "no request may be issued during the suspension window (Req 7.10)")

	// A full batch during the window reaches the batch-size trigger and still
	// transmits nothing: the trigger is not a way around the suspension.
	sd.emit(sdBatchSize)
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped >= sdBatchSize+sdBatchSize })
	if !ok {
		t.Fatalf("Dropped = %d after a full batch during the suspension, want at least %d (%+v)",
			snap.Dropped, 2*sdBatchSize, snap)
	}
	server.assertRequests(t, 1, "the batch-size trigger must not transmit during the suspension (Req 7.10)")
	sd.assertState(t, stateSuspended, "still inside the 15-minute window")

	// A flush boundary *before* the window elapses resumes nothing.
	sd.clock.Advance(suspensionWindow - sdFlushEvery)
	time.Sleep(sdSettle)
	server.assertRequests(t, 1, "a flush boundary before the window elapsed must transmit nothing (Req 7.10)")
	sd.assertState(t, stateSuspended, "one flush interval short of the window")

	// One event, held pending, so the resuming boundary has something to send.
	sd.emit(1)
	if _, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.InFlight == 1 }); !ok {
		t.Fatalf("the event emitted before the resumption was never batched (%+v)", sd.Snapshot())
	}

	// The first flush boundary after the 15 minutes resumes transmission and
	// sends the pending batch (Req 7.10).
	sd.clock.Advance(sdFlushEvery)

	if !server.waitRequests(2) {
		t.Fatalf("no request was issued at the first flush boundary after the %s window: %d requests (Req 7.10)",
			suspensionWindow, server.requests())
	}
	if !sd.waitState(stateEnabled) {
		t.Fatalf("state = %s after the window elapsed, want enabled (Req 7.10)", sdStateName(sd.state.Load()))
	}
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.Delivered == 1 })
	if !ok {
		t.Fatalf("Delivered = %d after the resumption, want 1 (%+v)", snap.Delivered, snap)
	}
	if snap.InFlight != 0 {
		t.Errorf("InFlight = %d after the resumed batch was accepted, want 0", snap.InFlight)
	}
	assertCounterIdentities(t, "after the suspension window elapsed and delivery resumed", snap)
	server.assertRequests(t, 2, "the resumption is one request, not a replay of the window")

	// The window was driven entirely on the fake clock: exactly 15 minutes of
	// simulated time, and no wall-clock waiting (Req 12.2).
	if elapsed := sd.clock.Now().Sub(fakeClockEpoch); elapsed != suspensionWindow {
		t.Errorf("the fake clock advanced %s, want exactly %s", elapsed, suspensionWindow)
	}
	// Still exactly one delivery diagnostic: resuming is silent, and no second 402
	// ERROR was written for the batches dropped during the window.
	//
	// The periodic health summaries task 8.11 added are excluded rather than
	// counted. Advancing 15 minutes of simulated time crosses three 5-minute
	// evaluation boundaries, and Req 11.6 requires a summary at each one in which
	// a counter moved — which is every one of them here, since events were
	// dropped throughout. Req 7.10's "one diagnostic" is about the suspension, so
	// the count below is of everything that is not a summary.
	var delivery []string
	for _, line := range sd.diag.lines() {
		if !strings.Contains(line, healthSummaryMessage) {
			delivery = append(delivery, line)
		}
	}
	if got := len(delivery); got != 1 {
		t.Errorf("wrote %d non-summary Self_Diagnostics over the whole suspension, want exactly 1 (Req 7.10): %q",
			got, sd.diag.text())
	}
}

// ---------------------------------------------------------------------------
// HTTP 403: permanent disable (Req 7.8, 7.9)
// ---------------------------------------------------------------------------

// TestSD403DrainsTheQueueAndTransmitsNothingEverAgain covers Req 7.8 and 7.9: the
// batch is discarded, every Log_Event still in the Event_Queue is discarded and
// counted, the Shipper is disabled for the remaining lifetime of the process, one
// ERROR states that the Source_Token was rejected, and no HTTP request is issued
// afterwards — at a flush boundary, from a later admission, or during the close
// of the delivery loop.
//
// The gate is what makes the drain observable: the delivery goroutine is parked
// inside Send while the queue is filled, so the queue is provably non-empty at
// the instant the 403 is classified.
func TestSD403DrainsTheQueueAndTransmitsNothingEverAgain(t *testing.T) {
	const queued = 6

	server := newSDServer(t, http.StatusForbidden, http.StatusAccepted).withGate(t)
	sd := newSDShipper(t, server)

	// The first batch reaches the endpoint and is held there.
	sd.emit(sdBatchSize)
	if !server.waitRequests(1) {
		t.Fatalf("the first Delivery_Batch never reached the endpoint within %s", sdWaitTimeout)
	}

	// With the delivery goroutine parked in Send, these events can only sit in
	// the Event_Queue: enqueue is a non-blocking channel send, so by the time
	// emit returns they are all resident.
	sd.emit(queued)
	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.QueueDepth == queued })
	if !ok {
		t.Fatalf("QueueDepth = %d before the 403, want %d resident events (%+v)", snap.QueueDepth, queued, snap)
	}
	if snap.InFlight != sdBatchSize {
		t.Fatalf("InFlight = %d, want the %d events of the held batch (%+v)", snap.InFlight, sdBatchSize, snap)
	}
	assertCounterIdentities(t, "with a batch in flight and events queued", snap)

	server.releaseGate() // the held response completes as 403

	if !sd.waitState(stateDisabled) {
		t.Fatalf("state = %s after HTTP 403, want disabled (Req 7.8)", sdStateName(sd.state.Load()))
	}

	// Req 7.8: the batch and every queued Log_Event are discarded and counted.
	const discarded = sdBatchSize + queued
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped == discarded })
	if !ok {
		t.Fatalf("Dropped = %d after HTTP 403, want %d (the batch of %d plus %d queued) (%+v)",
			snap.Dropped, discarded, sdBatchSize, queued, snap)
	}
	if got := snap.DroppedByReason[reasonNonRetryable]; got != discarded {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d (Req 7.8)", got, discarded)
	}
	if snap.Delivered != 0 || snap.InFlight != 0 || snap.QueueDepth != 0 {
		t.Errorf("Delivered = %d, InFlight = %d, QueueDepth = %d after the drain, want 0, 0, 0 (%+v)",
			snap.Delivered, snap.InFlight, snap.QueueDepth, snap)
	}
	assertCounterIdentities(t, "after HTTP 403 drained the queue", snap)
	if got := len(sd.queue); got != 0 {
		t.Errorf("the Event_Queue still holds %d events, want 0: the drain must empty it (Req 7.8)", got)
	}
	server.assertRequests(t, 1, "403 must not be retransmitted")

	// Exactly one ERROR, stating that the token was rejected and never naming it
	// (Req 7.8, 7.13, 10.4).
	rec := sd.diag.only(t)
	if rec["level"] != "ERROR" {
		t.Errorf("the 403 diagnostic level = %v, want ERROR (Req 7.8)", rec["level"])
	}
	message, _ := rec["msg"].(string)
	lowered := strings.ToLower(message)
	if !strings.Contains(lowered, "source token") || !strings.Contains(lowered, "rejected") {
		t.Errorf("the 403 diagnostic message = %q, want it to state that the Source_Token was rejected (Req 7.8)", message)
	}
	assertDiagField(t, rec, "status", http.StatusForbidden, "Req 7.8")
	assertDiagField(t, rec, "discarded_events", discarded, "Req 7.8")

	// Req 7.9, at a flush interval boundary: zero requests, however many
	// boundaries pass.
	for i := 0; i < 3; i++ {
		sd.clock.Advance(sdFlushEvery)
	}
	time.Sleep(sdSettle)
	server.assertRequests(t, 1, "a disabled Shipper must transmit nothing at flush boundaries (Req 7.9)")

	// Req 8.9: later admissions are discarded at enqueue, attributed to
	// reasonNonRetryable, and still transmit nothing.
	sd.emit(sdBatchSize)
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped == discarded+sdBatchSize })
	if !ok {
		t.Fatalf("Dropped = %d after admissions to a disabled Shipper, want %d (%+v)",
			snap.Dropped, discarded+sdBatchSize, snap)
	}
	if got := snap.DroppedByReason[reasonNonRetryable]; got != discarded+sdBatchSize {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d: an enqueue while disabled is non-retryable (Req 8.9)",
			got, discarded+sdBatchSize)
	}
	assertCounterIdentities(t, "after admissions to a disabled Shipper", snap)
	server.assertRequests(t, 1, "an admission to a disabled Shipper must transmit nothing (Req 7.9)")

	// Req 7.9, during the close of the delivery loop: still zero requests.
	sd.stop()
	time.Sleep(sdSettle)
	server.assertRequests(t, 1, "a disabled Shipper must transmit nothing during the close operation (Req 7.9)")

	if got := len(sd.diag.lines()); got != 1 {
		t.Errorf("wrote %d Self_Diagnostics, want exactly 1 for the whole disable (Req 7.8): %q",
			got, sd.diag.text())
	}
}

// ---------------------------------------------------------------------------
// Retry (Req 7.2, 7.4, 7.6, 7.7, 7.12)
// ---------------------------------------------------------------------------

// TestSD429RetriesAfterTheHeaderDelayAndSucceeds covers Req 7.2 and the 429 half
// of Req 7.4: a 429 is retried, the wait comes from the injected clock, the
// Retry-After header replaces the computed base, the retried-batch counter
// advances by one, and the retransmission carrying the same events is delivered.
//
// It also covers Req 7.12 for a backoff: an event admitted while the delay is
// outstanding is accepted into the Event_Queue.
func TestSD429RetriesAfterTheHeaderDelayAndSucceeds(t *testing.T) {
	// 7 s is inside the 1-300 s clamp and is not any value on the doubling
	// schedule, so a delay of 7 s can only have come from the header.
	const headerDelay = 7 * time.Second

	server := newSDServer(t, http.StatusTooManyRequests, http.StatusAccepted).withRetryAfter("7")
	sd := newSDShipper(t, server)

	sd.emit(sdBatchSize)

	if !server.waitRequests(1) {
		t.Fatalf("the first attempt never reached the endpoint within %s", sdWaitTimeout)
	}
	if !sd.clock.WaitAfters(1, sdWaitTimeout) {
		t.Fatalf("no backoff was scheduled on the injected clock after HTTP 429 (Req 7.2, 12.2)")
	}
	if got := sd.clock.AfterDurations(); len(got) != 1 || got[0] != headerDelay {
		t.Fatalf("the clock recorded %v, want exactly [%v]: the Retry-After header replaces the base for 429 (Req 7.4)",
			got, headerDelay)
	}

	// Mid-backoff: the events are still in flight, neither delivered nor
	// dropped. The retried-batch counter has not moved yet, because Req 7.2
	// counts a retransmission and not a retryable attempt — and a wait that a
	// Close cuts short produces no retransmission at all (Req 9.12).
	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.InFlight == sdBatchSize })
	if !ok {
		t.Fatalf("InFlight = %d during the backoff, want %d: a retryable attempt is not terminal (%+v)",
			snap.InFlight, sdBatchSize, snap)
	}
	if snap.RetriedBatches != 0 {
		t.Errorf("RetriedBatches = %d before the delay elapsed, want 0: the counter follows the retransmission (Req 7.2)",
			snap.RetriedBatches)
	}
	if snap.Delivered != 0 || snap.Dropped != 0 {
		t.Errorf("Delivered = %d, Dropped = %d during the backoff, want 0 and 0 (%+v)",
			snap.Delivered, snap.Dropped, snap)
	}
	assertCounterIdentities(t, "during the 429 backoff", snap)
	server.assertRequests(t, 1, "the retransmission must wait for the delay to elapse (Req 7.2)")

	// Req 7.12: enqueue stays open while the delay is outstanding. The delivery
	// goroutine is parked in the wait, so the event can only be in the queue.
	sd.emit(1)
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.QueueDepth == 1 })
	if !ok {
		t.Fatalf("QueueDepth = %d during the backoff, want 1: enqueue must stay open (Req 7.12) (%+v)",
			snap.QueueDepth, snap)
	}
	assertCounterIdentities(t, "with an event accepted during the backoff", snap)

	sd.clock.Advance(headerDelay)

	if !server.waitRequests(2) {
		t.Fatalf("the retransmission never arrived after the delay elapsed: %d requests (Req 7.2)", server.requests())
	}
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.Delivered == sdBatchSize })
	if !ok {
		t.Fatalf("Delivered = %d after the retransmission was accepted, want %d (%+v)",
			snap.Delivered, sdBatchSize, snap)
	}
	if snap.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0: a batch accepted on retry loses nothing (%+v)", snap.Dropped, snap)
	}
	if snap.RetriedBatches != 1 || snap.AbandonedBatches != 0 {
		t.Errorf("RetriedBatches = %d, AbandonedBatches = %d, want 1 and 0 (%+v)",
			snap.RetriedBatches, snap.AbandonedBatches, snap)
	}
	// The event admitted during the backoff is picked up once the retry
	// finishes: one event is below the batch size, so it is held pending.
	snap, ok = sd.waitSnapshot(func(s Snapshot) bool { return s.InFlight == 1 })
	if !ok {
		t.Fatalf("InFlight = %d after the retry completed, want the 1 event admitted during the backoff (%+v)",
			snap.InFlight, snap)
	}
	assertCounterIdentities(t, "after the 429 retry was accepted", snap)
	server.assertRequests(t, 2, "one attempt plus one retransmission")

	// No diagnostic at all: Req 7.2 attaches none to a retried attempt, and the
	// eventual 202 is silent (Req 7.1).
	if got := sd.diag.lines(); len(got) != 0 {
		t.Errorf("wrote %d Self_Diagnostics for a retried-then-delivered batch, want 0 (Req 7.1, 7.2): %q",
			len(got), sd.diag.text())
	}
}

// TestSD5xxRetriedToExhaustionAbandonsTheBatch covers Req 7.6 and 7.7 and the
// other half of Req 7.4: a 5xx is retried on the doubling schedule — *ignoring*
// the Retry-After header it carries, which is honoured for 429 alone — the batch
// appears in exactly 5 requests, and the fifth retryable attempt abandons it with
// the abandoned-batch accounting and exactly one ERROR.
//
// It also pins the in-flight release to exactly once. A sentinel event is held in
// flight for the duration, so a second release on the abandonment path would show
// up as an InFlight of 0 rather than being hidden by the counter's clamp at zero.
func TestSD5xxRetriedToExhaustionAbandonsTheBatch(t *testing.T) {
	// 120 s is a legal Retry-After that no doubling step produces, so honouring
	// it anywhere would be visible in the recorded schedule.
	server := newSDServer(t, http.StatusInternalServerError).withRetryAfter("120")
	sd := newSDShipper(t, server)

	// The sentinel: one admitted event held in flight by the test, standing in
	// for an event of some other batch. It is never delivered or dropped, so it
	// must still be in flight when everything else has settled.
	sd.counters.addAdmitted(1)
	sd.counters.addInFlight(1)

	sd.emit(sdBatchSize)

	// Walk the backoff schedule: wait for each delay to be scheduled on the
	// injected clock, then advance exactly that far.
	wantSchedule := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, want := range wantSchedule {
		if !sd.clock.WaitAfters(i+1, sdWaitTimeout) {
			t.Fatalf("only %d backoffs were scheduled, want %d: attempt %d never waited (Req 7.2)",
				sd.clock.AfterCount(), i+1, i+2)
		}
		got := sd.clock.AfterDurations()
		if got[i] != want {
			t.Fatalf("backoff %d = %v, want %v (Req 7.4: a 5xx follows the doubling schedule and ignores Retry-After); schedule so far %v",
				i+1, got[i], want, got)
		}
		sd.clock.Advance(want)
	}

	if !server.waitRequests(maxTransmitAttempts) {
		t.Fatalf("the endpoint received %d requests, want %d attempts (Req 7.6)", server.requests(), maxTransmitAttempts)
	}
	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.AbandonedBatches == 1 })
	if !ok {
		t.Fatalf("AbandonedBatches = %d after the fifth retryable attempt, want 1 (Req 7.7) (%+v)",
			snap.AbandonedBatches, snap)
	}

	// Req 7.6: the events of one batch appear in at most 5 requests. Nothing
	// further is transmitted, and no sixth backoff is scheduled.
	time.Sleep(sdSettle)
	server.assertRequests(t, maxTransmitAttempts, "one batch appears in at most 5 requests (Req 7.6)")
	if got := sd.clock.AfterDurations(); len(got) != len(wantSchedule) {
		t.Errorf("the clock recorded %d backoffs %v, want exactly %d: %v",
			len(got), got, len(wantSchedule), wantSchedule)
	}

	snap = sd.Snapshot()
	if snap.RetriedBatches != uint64(maxTransmitAttempts-1) {
		t.Errorf("RetriedBatches = %d, want %d retransmissions (Req 7.2)", snap.RetriedBatches, maxTransmitAttempts-1)
	}
	if snap.Dropped != sdBatchSize || snap.DroppedByReason[reasonAbandonedBatch] != sdBatchSize {
		t.Errorf("Dropped = %d (abandoned reason %d), want %d and %d (Req 7.7)",
			snap.Dropped, snap.DroppedByReason[reasonAbandonedBatch], sdBatchSize, sdBatchSize)
	}
	if snap.DroppedByReason[reasonNonRetryable] != 0 {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want 0: an exhausted batch is abandoned, not non-retryable (Req 7.7)",
			snap.DroppedByReason[reasonNonRetryable])
	}
	if snap.Delivered != 0 {
		t.Errorf("Delivered = %d, want 0", snap.Delivered)
	}
	// The whole point of the sentinel: exactly one release of the batch's events.
	// A second addInFlight(-2) would have driven this to 0.
	if snap.InFlight != 1 {
		t.Errorf("InFlight = %d, want 1 (the sentinel): the abandoned batch's in-flight release must happen exactly once (Req 11.3, 11.4)",
			snap.InFlight)
	}
	sd.assertState(t, stateEnabled, "a 5xx neither suspends nor disables")
	assertCounterIdentities(t, "after the batch was abandoned", snap)

	// Exactly one ERROR, naming the discarded count, the attempt count, and the
	// last observed status (Req 7.7).
	rec := sd.diag.only(t)
	if rec["level"] != "ERROR" {
		t.Errorf("the abandonment diagnostic level = %v, want ERROR (Req 7.7)", rec["level"])
	}
	assertDiagField(t, rec, "discarded_events", sdBatchSize, "Req 7.7")
	assertDiagField(t, rec, "attempts", maxTransmitAttempts, "Req 7.7")
	assertDiagField(t, rec, "status", http.StatusInternalServerError, "Req 7.7")
}

// ---------------------------------------------------------------------------
// Delivered statuses and non-retryable drops (Req 7.14, 7.11, 7.15)
// ---------------------------------------------------------------------------

// TestSD200DeliversTheBatchAndWarnsAboutTheStatus covers Req 7.14: a 2xx other
// than 202 still delivers the batch, and produces exactly one WARN naming the
// status.
func TestSD200DeliversTheBatchAndWarnsAboutTheStatus(t *testing.T) {
	server := newSDServer(t, http.StatusOK)
	sd := newSDShipper(t, server)

	sd.emit(sdBatchSize)

	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Delivered == sdBatchSize })
	if !ok {
		t.Fatalf("Delivered = %d after HTTP 200, want %d (Req 7.14) (%+v)", snap.Delivered, sdBatchSize, snap)
	}
	if snap.Dropped != 0 || snap.InFlight != 0 {
		t.Errorf("Dropped = %d, InFlight = %d after HTTP 200, want 0 and 0 (%+v)", snap.Dropped, snap.InFlight, snap)
	}
	if snap.RetriedBatches != 0 {
		t.Errorf("RetriedBatches = %d, want 0: a 2xx is not retried (Req 7.14)", snap.RetriedBatches)
	}
	assertCounterIdentities(t, "after HTTP 200 delivered the batch", snap)
	server.assertRequests(t, 1, "a delivered batch is transmitted once")
	sd.assertState(t, stateEnabled, "a 2xx changes no state")

	rec := sd.diag.only(t)
	if rec["level"] != "WARN" {
		t.Errorf("the 200 diagnostic level = %v, want WARN (Req 7.14)", rec["level"])
	}
	assertDiagField(t, rec, "status", http.StatusOK, "Req 7.14")
	if _, ok := rec["discarded_events"]; ok {
		t.Errorf("the 200 diagnostic reports discarded events for a delivered batch: %v", rec)
	}
}

// TestSD202DeliversTheBatchSilently covers Req 7.1: the documented success status
// delivers the batch and writes nothing at all.
func TestSD202DeliversTheBatchSilently(t *testing.T) {
	server := newSDServer(t, http.StatusAccepted)
	sd := newSDShipper(t, server)

	sd.emit(sdBatchSize)

	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Delivered == sdBatchSize })
	if !ok {
		t.Fatalf("Delivered = %d after HTTP 202, want %d (Req 7.1) (%+v)", snap.Delivered, sdBatchSize, snap)
	}
	assertCounterIdentities(t, "after HTTP 202 delivered the batch", snap)
	server.assertRequests(t, 1, "a delivered batch is transmitted once")

	time.Sleep(sdSettle)
	if got := sd.diag.lines(); len(got) != 0 {
		t.Errorf("wrote %d Self_Diagnostics for a 202, want 0 (Req 7.1): %q", len(got), sd.diag.text())
	}
}

// TestSDNonRetryableStatusesDropTheBatchWithOneError covers Req 7.11 and 7.15:
// 406 and 413 discard the batch with an ERROR naming the status *and* the
// discarded count, and every other non-retryable status discards it with an ERROR
// naming the status. None of them is retransmitted, and none of them changes the
// delivery state.
func TestSDNonRetryableStatusesDropTheBatchWithOneError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		namesCount bool
		req        string
	}{
		{name: "406 not acceptable", status: http.StatusNotAcceptable, namesCount: true, req: "Req 7.11"},
		{name: "413 payload too large", status: http.StatusRequestEntityTooLarge, namesCount: true, req: "Req 7.11"},
		{name: "404 not found", status: http.StatusNotFound, req: "Req 7.15"},
		{name: "400 bad request", status: http.StatusBadRequest, req: "Req 7.15"},
		{name: "301 moved permanently", status: http.StatusMovedPermanently, req: "Req 7.15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newSDServer(t, tc.status)
			sd := newSDShipper(t, server)

			sd.emit(sdBatchSize)

			snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped == sdBatchSize })
			if !ok {
				t.Fatalf("Dropped = %d after HTTP %d, want %d (%s) (%+v)",
					snap.Dropped, tc.status, sdBatchSize, tc.req, snap)
			}
			if got := snap.DroppedByReason[reasonNonRetryable]; got != sdBatchSize {
				t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d (%s)", got, sdBatchSize, tc.req)
			}
			if snap.Delivered != 0 || snap.InFlight != 0 {
				t.Errorf("Delivered = %d, InFlight = %d, want 0 and 0 (%+v)", snap.Delivered, snap.InFlight, snap)
			}
			if snap.RetriedBatches != 0 || snap.AbandonedBatches != 0 {
				t.Errorf("RetriedBatches = %d, AbandonedBatches = %d, want 0 and 0: HTTP %d is not retryable (%s)",
					snap.RetriedBatches, snap.AbandonedBatches, tc.status, tc.req)
			}
			assertCounterIdentities(t, "after a non-retryable status dropped the batch", snap)

			// No retransmission, and no request at a later flush boundary
			// either: the batch is gone, not deferred.
			sd.clock.Advance(sdFlushEvery)
			time.Sleep(sdSettle)
			server.assertRequests(t, 1, "a non-retryable status must not be retransmitted")

			// The Shipper stays usable: only 402 and 403 change the state.
			sd.assertState(t, stateEnabled, "a plain non-retryable status neither suspends nor disables")

			rec := sd.diag.only(t)
			if rec["level"] != "ERROR" {
				t.Errorf("the HTTP %d diagnostic level = %v, want ERROR (%s)", tc.status, rec["level"], tc.req)
			}
			assertDiagField(t, rec, "status", tc.status, tc.req)
			if tc.namesCount {
				assertDiagField(t, rec, "discarded_events", sdBatchSize, tc.req)
			}
			if got, ok := rec["endpoint"].(string); !ok || !strings.HasPrefix(got, "http://127.0.0.1") {
				t.Errorf("diagnostic endpoint = %v, want the sanitized loopback endpoint (Req 10.5)", rec["endpoint"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The state transitions, driven directly
// ---------------------------------------------------------------------------

// TestSDSuspensionAndDisableTransitionsAreOrdered drives the two transitions
// without a server, which is the only way to check the cases the wire cannot
// produce: that a suspension can never overwrite a permanent disable (Req 7.9),
// that a disabled Shipper is never re-enabled by an elapsed suspension window,
// and that the resume boundary honours the deadline exactly.
//
// Every call here happens on the test goroutine, which owns suspendUntil for the
// duration because no delivery goroutine is running.
func TestSDSuspensionAndDisableTransitionsAreOrdered(t *testing.T) {
	t.Run("a suspension expires exactly at the deadline", func(t *testing.T) {
		clock := newFakeClock()
		s := NewShipper(Config{
			Enabled: true, MinLevel: slog.LevelError, IngestURL: testShipperURL,
			SourceToken: sdToken, QueueSize: 4, BatchSize: 2, FlushEvery: sdFlushEvery,
			DiagWriter: io.Discard, Clock: clock,
		})

		s.suspendTransmission()
		if got := s.state.Load(); got != stateSuspended {
			t.Fatalf("state = %s, want suspended (Req 7.10)", sdStateName(got))
		}

		// One nanosecond short of the window: still suspended.
		clock.Advance(suspensionWindow - time.Nanosecond)
		s.resumeIfSuspensionElapsed()
		if got := s.state.Load(); got != stateSuspended {
			t.Errorf("state = %s one nanosecond before the deadline, want suspended (Req 7.10)", sdStateName(got))
		}

		clock.Advance(time.Nanosecond)
		s.resumeIfSuspensionElapsed()
		if got := s.state.Load(); got != stateEnabled {
			t.Errorf("state = %s at the deadline, want enabled (Req 7.10)", sdStateName(got))
		}
	})

	t.Run("a suspension never overwrites a permanent disable", func(t *testing.T) {
		clock := newFakeClock()
		s := NewShipper(Config{
			Enabled: true, MinLevel: slog.LevelError, IngestURL: testShipperURL,
			SourceToken: sdToken, QueueSize: 4, BatchSize: 2, FlushEvery: sdFlushEvery,
			DiagWriter: io.Discard, Clock: clock,
		})

		if got := s.disableAfterTokenRejection(); got != 0 {
			t.Errorf("draining an empty queue discarded %d events, want 0", got)
		}
		if got := s.state.Load(); got != stateDisabled {
			t.Fatalf("state = %s, want disabled (Req 7.8)", sdStateName(got))
		}

		// A 402 from a batch that was already in flight must not resurrect
		// transmission 15 minutes later (Req 7.9).
		s.suspendTransmission()
		if got := s.state.Load(); got != stateDisabled {
			t.Errorf("state = %s after a 402 arrived post-403, want disabled (Req 7.9)", sdStateName(got))
		}

		// Nor may an elapsed window re-enable it.
		clock.Advance(2 * suspensionWindow)
		s.resumeIfSuspensionElapsed()
		if got := s.state.Load(); got != stateDisabled {
			t.Errorf("state = %s after the window elapsed, want disabled for the remaining lifetime (Req 7.9)",
				sdStateName(got))
		}
		if !s.cannotTransmit() {
			t.Error("cannotTransmit() = false for a disabled Shipper, want true (Req 7.9)")
		}
	})

	t.Run("the drain counts every event it removes", func(t *testing.T) {
		const queued = 5

		s := NewShipper(Config{
			Enabled: true, MinLevel: slog.LevelError, IngestURL: testShipperURL,
			SourceToken: sdToken, QueueSize: 8, BatchSize: 2, FlushEvery: sdFlushEvery,
			DiagWriter: io.Discard, Clock: newFakeClock(),
		})

		for i := 0; i < queued; i++ {
			s.counters.addAdmitted(1)
			s.enqueue(&Event{Message: "queued"})
		}
		if snap := s.Snapshot(); snap.QueueDepth != queued {
			t.Fatalf("QueueDepth = %d, want %d before the drain (%+v)", snap.QueueDepth, queued, snap)
		}

		if got := s.disableAfterTokenRejection(); got != queued {
			t.Errorf("the drain reported %d discarded events, want %d (Req 7.8)", got, queued)
		}
		if got := len(s.queue); got != 0 {
			t.Errorf("the Event_Queue still holds %d events after the drain, want 0 (Req 7.8)", got)
		}

		snap := s.Snapshot()
		if snap.Dropped != queued || snap.DroppedByReason[reasonNonRetryable] != queued {
			t.Errorf("Dropped = %d (non-retryable %d), want %d and %d (Req 7.8)",
				snap.Dropped, snap.DroppedByReason[reasonNonRetryable], queued, queued)
		}
		if snap.InFlight != 0 || snap.QueueDepth != 0 {
			t.Errorf("InFlight = %d, QueueDepth = %d after the drain, want 0 and 0: drained events were never in flight (%+v)",
				snap.InFlight, snap.QueueDepth, snap)
		}
		assertCounterIdentities(t, "after the 403 drain", snap)

		// A second drain finds nothing and counts nothing, so the accounting is
		// not double-charged if the path is somehow re-entered.
		if got := s.disableAfterTokenRejection(); got != 0 {
			t.Errorf("a second drain reported %d discarded events, want 0", got)
		}
		if got := s.Snapshot().Dropped; got != queued {
			t.Errorf("Dropped = %d after a second drain, want %d", got, queued)
		}
	})
}
