package logging

// Example tests for the delivery goroutine: logging/shipper.go's run, transmit,
// and fits.
//
// What this file is about, and why it needs a server at all: every other test in
// this package can assert on counters or on a queue, but the three triggers of
// Req 5.5, 5.7, and 5.14 are only observable as *HTTP requests* — how many, and
// which events each one carried. So each test below drives a real Shipper against
// an httptest.Server that records every body it receives, and asserts on that
// record.
//
// Two disciplines hold throughout:
//
//   - **No sleeping for time to pass.** The flush interval is driven by advancing
//     the injected fakeClock (Req 12.2). The only real sleeps are waitFor's 1 ms
//     polls, which synchronise with the delivery goroutine rather than wait out a
//     duration.
//   - **No network.** Every endpoint is loopback, checked by
//     assertLoopbackEndpoint rather than assumed (Req 12.3).
//
// Property 6 (batching confluence, task 6.6) and Property 15 (the 1 MiB body cap,
// task 6.7) are appended to this file. Every identifier here is prefixed
// `delivery`/`Delivery` so they can be added without collision.

import (
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

// deliveryWaitTimeout bounds every wait for the delivery goroutine to reach a
// state. It is only ever waited out when a test is failing, so its size costs
// nothing in the passing case.
const deliveryWaitTimeout = 2 * time.Second

// ---------------------------------------------------------------------------
// A recording ingest endpoint
// ---------------------------------------------------------------------------

// deliveryServer is an httptest.Server that records the body of every request it
// receives and answers each one with a fixed status.
//
// Bodies are kept whole rather than reduced to a count, because the assertions
// that matter are about *which* events were in *which* request — batch splits,
// enqueue order (Req 5.8) — and about the exact byte size of the body (Req 5.14).
type deliveryServer struct {
	*httptest.Server
	status int

	mu     sync.Mutex
	bodies [][]byte
}

// newDeliveryServer starts a recording endpoint answering every request with
// status, and registers its shutdown.
func newDeliveryServer(t *testing.T, status int) *deliveryServer {
	t.Helper()

	server := &deliveryServer{status: status}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		server.mu.Lock()
		server.bodies = append(server.bodies, body)
		server.mu.Unlock()
		w.WriteHeader(server.status)
	}))
	t.Cleanup(server.Close)

	assertLoopbackEndpoint(t, server.URL) // Req 12.3
	return server
}

// requests reports how many requests have arrived.
func (s *deliveryServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// bodySizes returns the byte length of every recorded body, in arrival order.
func (s *deliveryServer) bodySizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sizes := make([]int, len(s.bodies))
	for i, body := range s.bodies {
		sizes[i] = len(body)
	}
	return sizes
}

// batches decodes every recorded body into the messages of the events it
// carried, preserving both request order and within-request order, so a test can
// assert the batch boundaries and the enqueue order in one value (Req 5.8).
func (s *deliveryServer) batches(t *testing.T) [][]string {
	t.Helper()

	s.mu.Lock()
	bodies := make([][]byte, len(s.bodies))
	copy(bodies, s.bodies)
	s.mu.Unlock()

	out := make([][]string, 0, len(bodies))
	for i, body := range bodies {
		var events []struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &events); err != nil {
			t.Fatalf("request %d body does not decode as a JSON array of events: %v (%d bytes)", i, err, len(body))
		}
		messages := make([]string, len(events))
		for j, ev := range events {
			messages[j] = ev.Message
		}
		out = append(out, messages)
	}
	return out
}

// waitRequests blocks until at least n requests have arrived, and reports
// whether they did.
func (s *deliveryServer) waitRequests(n int) bool {
	return waitFor(func() bool { return s.requests() >= n }, deliveryWaitTimeout)
}

// ---------------------------------------------------------------------------
// Shipper construction
// ---------------------------------------------------------------------------

// deliveryConfig builds a Config aimed at a loopback endpoint.
//
// It is assembled by hand rather than through ResolveConfig for one reason:
// Req 1.6 requires an https scheme, and an httptest.Server is http. Every other
// field carries the value ResolveConfig would have produced, so the Shipper under
// test behaves as the configured one does.
func deliveryConfig(url string, batchSize, queueSize int, clock Clock) Config {
	return Config{
		Enabled:     true,
		IngestURL:   url,
		SourceToken: testShipperToken,
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   batchSize,
		FlushEvery:  defaultFlushSeconds * time.Second,
		QueueSize:   queueSize,
		DiagWriter:  io.Discard,
		Clock:       clock,
	}
}

// startDeliveryShipper starts a Shipper's delivery goroutine and waits until its
// flush ticker exists, so a test that advances the clock cannot advance it before
// the ticker was registered and then wonder why no tick arrived.
//
// It releases the goroutine through closeDone, which is the edge run itself
// selects on; Close is task 8.8.
func startDeliveryShipper(t *testing.T, cfg Config, clock *fakeClock) *Shipper {
	t.Helper()

	s := NewShipper(cfg)
	s.Start()
	t.Cleanup(func() { close(s.closeDone) })

	if !clock.WaitTickers(1, deliveryWaitTimeout) {
		t.Fatalf("the delivery goroutine registered no flush ticker within %s", deliveryWaitTimeout)
	}
	return s
}

// deliveryLogger returns a logger that reaches the Shipper the way production
// does: through the fan-out, so every record is also written to a console buffer
// and the enqueued counter is incremented by Handle.
func deliveryLogger(s *Shipper) *slog.Logger {
	return slog.New(NewFanoutHandler(slog.NewJSONHandler(io.Discard, nil), s.Handler()))
}

// deliveryEmit emits n ERROR records whose messages are stable and ordered, so a
// recorded batch identifies exactly which events it carried.
func deliveryEmit(logger *slog.Logger, prefix string, n int) {
	for i := 0; i < n; i++ {
		logger.Error(deliveryMessage(prefix, i))
	}
}

func deliveryMessage(prefix string, i int) string {
	return prefix + "-" + string(rune('a'+i%26)) + "-" + itoaSmall(i)
}

// itoaSmall renders a small non-negative int without pulling strconv into this
// file's imports for one call.
func itoaSmall(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// deliveryExpectedMessages returns the messages deliveryEmit produces, in order.
func deliveryExpectedMessages(prefix string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = deliveryMessage(prefix, i)
	}
	return out
}

// assertDeliveryBatches fails unless the server received exactly want.
func assertDeliveryBatches(t *testing.T, server *deliveryServer, want [][]string) {
	t.Helper()

	got := server.batches(t)
	if len(got) != len(want) {
		t.Fatalf("received %d requests %v, want %d requests %v", len(got), got, len(want), want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("request %d carried %d events %v, want %d events %v", i, len(got[i]), got[i], len(want[i]), want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("request %d event %d = %q, want %q (batch %v, want %v)", i, j, got[i][j], want[i][j], got[i], want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The batch-size trigger (Req 5.5)
// ---------------------------------------------------------------------------

// TestDeliveryBatchSizeTriggerTransmitsWithoutWaitingForTheFlushInterval covers
// Req 5.5: reaching the maximum Delivery_Batch size transmits at once, and the
// flush interval is not what releases it.
//
// The clock is never advanced, so the flush ticker cannot possibly have fired.
// Any request that arrives therefore came from the count reaching BatchSize —
// and it arrives in the same loop iteration that observed the count, which is
// how the 100 ms bound of Req 5.5 is met: there is no wait to bound.
func TestDeliveryBatchSizeTriggerTransmitsWithoutWaitingForTheFlushInterval(t *testing.T) {
	const batchSize = 4

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, batchSize, 16, clock), clock)

	start := time.Now()
	deliveryEmit(deliveryLogger(s), "batchsize", batchSize)

	if !server.waitRequests(1) {
		t.Fatalf("no Delivery_Batch arrived within %s after the count reached BatchSize = %d (Req 5.5)",
			deliveryWaitTimeout, batchSize)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("the batch-size trigger took %s, want at most 100ms (Req 5.5)", elapsed)
	}

	assertDeliveryBatches(t, server, [][]string{deliveryExpectedMessages("batchsize", batchSize)})

	// The flush ticker plus the 5-minute health-summary ticker task 8.11 added
	// (Req 11.6). Both are registered by the delivery goroutine and neither has
	// fired, which is the claim that matters here: the clock never moved.
	if got := clock.TickerCount(); got != 2 {
		t.Errorf("TickerCount = %d, want the flush ticker and the health ticker", got)
	}
	if got := clock.Now(); !got.Equal(fakeClockEpoch) {
		t.Errorf("the clock moved to %s: this test must transmit without any interval elapsing", got)
	}

	if !waitFor(func() bool { return s.Snapshot().Delivered == batchSize }, deliveryWaitTimeout) {
		t.Fatalf("Delivered = %d, want %d", s.Snapshot().Delivered, batchSize)
	}
	snap := s.Snapshot()
	if snap.InFlight != 0 || snap.QueueDepth != 0 || snap.Dropped != 0 {
		t.Errorf("InFlight = %d, QueueDepth = %d, Dropped = %d after the batch was accepted, want 0, 0, 0 (%+v)",
			snap.InFlight, snap.QueueDepth, snap.Dropped, snap)
	}
	assertCounterIdentities(t, "after the batch-size trigger delivered one batch", snap)
}

// TestDeliveryPendingNeverExceedsBatchSize covers the bound Req 6.6 rests on: no
// Delivery_Batch ever carries more than BatchSize events, however many arrive.
//
// 10 events at BatchSize 3 means the trigger has to fire three times and leave a
// remainder, so a loop that appended before checking the length, or that checked
// with `==` instead of `>=`, would show up here as a batch of 4 or as a fourth
// request that never came.
func TestDeliveryPendingNeverExceedsBatchSize(t *testing.T) {
	const (
		batchSize = 3
		records   = 10
	)

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, batchSize, 32, clock), clock)

	deliveryEmit(deliveryLogger(s), "bound", records)

	if !server.waitRequests(records / batchSize) {
		t.Fatalf("received %d requests within %s, want %d full batches of %d",
			server.requests(), deliveryWaitTimeout, records/batchSize, batchSize)
	}

	expected := deliveryExpectedMessages("bound", records)
	assertDeliveryBatches(t, server, [][]string{expected[0:3], expected[3:6], expected[6:9]})

	// The remainder is held for the flush interval, not transmitted early.
	if got := server.requests(); got != records/batchSize {
		t.Errorf("received %d requests, want exactly %d: the remainder of %d event(s) must wait for a flush boundary (Req 5.7)",
			got, records/batchSize, records%batchSize)
	}

	if !waitFor(func() bool { return s.Snapshot().Delivered == 9 }, deliveryWaitTimeout) {
		t.Fatalf("Delivered = %d, want 9", s.Snapshot().Delivered)
	}
	snap := s.Snapshot()
	if snap.InFlight != 1 {
		t.Errorf("InFlight = %d, want 1: the tenth event is batched and not yet terminal (Req 11.3)", snap.InFlight)
	}
	assertCounterIdentities(t, "with a partial batch pending", snap)
}

// ---------------------------------------------------------------------------
// The flush tick (Req 5.7, 5.12)
// ---------------------------------------------------------------------------

// TestDeliveryFlushTickWithAnEmptyPendingMakesZeroRequests covers Req 5.12: the
// interval elapsing with nothing queued transmits nothing and the interval
// restarts.
//
// The restart is the part that a "zero requests" assertion alone would miss — a
// loop that stopped its ticker, or that only re-armed it after a transmission,
// would pass the first half of this test and fail the second.
func TestDeliveryFlushTickWithAnEmptyPendingMakesZeroRequests(t *testing.T) {
	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, 20, 8, clock), clock)

	for i := 0; i < 3; i++ {
		clock.Advance(defaultFlushSeconds * time.Second)
	}
	// Give a request that was about to be issued the chance to arrive. Polling
	// for zero would pass instantly and prove nothing.
	time.Sleep(20 * waitPollStep)

	if got := server.requests(); got != 0 {
		t.Fatalf("received %d requests after 3 empty flush intervals, want 0 (Req 5.12)", got)
	}
	if got := s.Snapshot(); got != (Snapshot{}) {
		t.Errorf("Snapshot = %+v after 3 empty flush intervals, want the zero Snapshot", got)
	}

	// The interval restarted: one event plus one more interval still flushes.
	deliveryEmit(deliveryLogger(s), "restart", 1)
	if !waitFor(func() bool { return s.Snapshot().InFlight == 1 }, deliveryWaitTimeout) {
		t.Fatalf("InFlight = %d, want the single event batched before the tick", s.Snapshot().InFlight)
	}
	clock.Advance(defaultFlushSeconds * time.Second)

	if !server.waitRequests(1) {
		t.Fatalf("no request arrived within %s after an interval with one event pending: the flush interval did not restart (Req 5.12)",
			deliveryWaitTimeout)
	}
	assertDeliveryBatches(t, server, [][]string{deliveryExpectedMessages("restart", 1)})
}

// TestDeliveryFlushTickTransmitsANonEmptyPendingBatch covers Req 5.7: the
// interval elapsing with events pending transmits them, in enqueue order, in one
// request, even though the count never reached BatchSize.
func TestDeliveryFlushTickTransmitsANonEmptyPendingBatch(t *testing.T) {
	const records = 3

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, 20, 8, clock), clock)

	deliveryEmit(deliveryLogger(s), "tick", records)

	// Synchronise on the events having been batched rather than on a duration:
	// advancing the clock before run received them would test nothing.
	if !waitFor(func() bool { return s.Snapshot().InFlight == records }, deliveryWaitTimeout) {
		t.Fatalf("InFlight = %d, want %d events batched before the tick", s.Snapshot().InFlight, records)
	}
	if got := server.requests(); got != 0 {
		t.Fatalf("received %d requests before any interval elapsed, want 0: %d events is below BatchSize", got, records)
	}

	clock.Advance(defaultFlushSeconds * time.Second)

	if !server.waitRequests(1) {
		t.Fatalf("no request arrived within %s after the flush interval elapsed with %d events pending (Req 5.7)",
			deliveryWaitTimeout, records)
	}
	assertDeliveryBatches(t, server, [][]string{deliveryExpectedMessages("tick", records)})

	if !waitFor(func() bool { return s.Snapshot().Delivered == records }, deliveryWaitTimeout) {
		t.Fatalf("Delivered = %d, want %d", s.Snapshot().Delivered, records)
	}
	assertCounterIdentities(t, "after a flush tick delivered a partial batch", s.Snapshot())
}

// ---------------------------------------------------------------------------
// HTTP 202 and the accounting identity (Req 7.1, 11.4)
// ---------------------------------------------------------------------------

// TestDelivery202AdvancesDeliveredAndKeepsTheAccountingIdentity covers Req 7.1
// and Req 11.4 together: an accepted batch moves its events out of in-flight and
// into delivered, once each, and every snapshot taken along the way balances.
//
// The identity is checked *while* delivery is in progress, not only at the end,
// because the ordering that makes it exact — release in-flight before recording
// the terminal outcome — is only observable mid-flight.
func TestDelivery202AdvancesDeliveredAndKeepsTheAccountingIdentity(t *testing.T) {
	const (
		batchSize = 5
		records   = 20
	)

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, batchSize, 64, clock), clock)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deliveryEmit(deliveryLogger(s), "identity", records)
	}()

	// Sample the identity continuously while batches are being transmitted.
	for {
		assertCounterIdentities(t, "while batches were in flight", s.Snapshot())
		select {
		case <-done:
			if !waitFor(func() bool { return s.Snapshot().Delivered == records }, deliveryWaitTimeout) {
				t.Fatalf("Delivered = %d, want %d (%+v)", s.Snapshot().Delivered, records, s.Snapshot())
			}
			snap := s.Snapshot()
			if snap.Enqueued != records {
				t.Errorf("Enqueued = %d, want %d admissions", snap.Enqueued, records)
			}
			if snap.Dropped != 0 || snap.InFlight != 0 || snap.QueueDepth != 0 {
				t.Errorf("Dropped = %d, InFlight = %d, QueueDepth = %d, want 0, 0, 0 (%+v)",
					snap.Dropped, snap.InFlight, snap.QueueDepth, snap)
			}
			if snap.RetriedBatches != 0 || snap.AbandonedBatches != 0 {
				t.Errorf("RetriedBatches = %d and AbandonedBatches = %d on the 202 path, want 0 and 0",
					snap.RetriedBatches, snap.AbandonedBatches)
			}
			assertCounterIdentities(t, "after every batch was accepted", snap)
			if got, want := server.requests(), records/batchSize; got != want {
				t.Errorf("received %d requests, want %d batches of %d", got, want, batchSize)
			}
			return
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// The 1 MiB body cap (Req 5.14)
// ---------------------------------------------------------------------------

// deliveryPaddedEvent builds an Event whose serialized form is at least size
// bytes, identified by message, with its serialized length cached the way
// capEventSize caches it at conversion time.
//
// capEventSize is deliberately *not* called: it would truncate the event to
// 65536 bytes (Req 3.12), and events that large cannot exercise a 1048576-byte
// body cap in a handful of them. The event is otherwise exactly what conversion
// produces, and the cached length is measured rather than invented.
func deliveryPaddedEvent(t *testing.T, message string, size int) *Event {
	t.Helper()

	ev := &Event{
		DT:          fakeClockEpoch.Format(time.RFC3339Nano),
		Level:       slog.LevelError.String(),
		Message:     message,
		Service:     defaultServiceName,
		Environment: productionEnvironment,
		Context:     map[string]any{"pad": strings.Repeat("p", size)},
	}
	measured, ok := serializedSize(ev)
	if !ok {
		t.Fatalf("hand-built event %q does not marshal", message)
	}
	if measured < size {
		t.Fatalf("padded event %q serialized to %d bytes, want at least %d", message, measured, size)
	}
	ev.SetSerializedLen(measured)
	return ev
}

// TestDeliveryBodyCapSplitsTheBatchAndRetainsTheOversizedEvent covers Req 5.14:
// an event that would raise the body above 1048576 bytes is not what gets
// transmitted — the batch goes without it, and it is retained for the next batch
// rather than dropped.
//
// The events are enqueued directly, with the admission counted the way Handle
// counts it, because the Handle path caps every event at 65536 bytes and could
// not reach the body cap with three events.
func TestDeliveryBodyCapSplitsTheBatchAndRetainsTheOversizedEvent(t *testing.T) {
	// 600 KiB each: two fit in no body, so every arrival after the first splits.
	const (
		padBytes = 600 * 1024
		events   = 3
	)

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	// BatchSize 20 so nothing here can be attributed to the count trigger.
	s := startDeliveryShipper(t, deliveryConfig(server.URL, 20, 8, clock), clock)

	want := make([][]string, 0, events)
	for i := 0; i < events; i++ {
		message := deliveryMessage("cap", i)
		s.counters.addAdmitted(1) // what Handle counts before conversion
		s.enqueue(deliveryPaddedEvent(t, message, padBytes))
		want = append(want, []string{message})
		if i == 0 {
			continue
		}
		// Each subsequent arrival flushes the single event that was pending.
		if !server.waitRequests(i) {
			t.Fatalf("received %d requests after event %d arrived, want %d: the body cap did not split the batch (Req 5.14)",
				server.requests(), i, i)
		}
	}

	// The third event is still pending: the cap flushed what came before it and
	// retained it (Req 5.14), so it reaches the wire at the next flush boundary.
	if got := server.requests(); got != events-1 {
		t.Fatalf("received %d requests, want %d: the last event must be retained, not transmitted", got, events-1)
	}
	if !waitFor(func() bool { return s.Snapshot().InFlight == 1 }, deliveryWaitTimeout) {
		t.Fatalf("InFlight = %d, want 1 retained event (%+v)", s.Snapshot().InFlight, s.Snapshot())
	}

	clock.Advance(defaultFlushSeconds * time.Second)
	if !server.waitRequests(events) {
		t.Fatalf("the retained event was not transmitted at the next flush boundary: %d requests, want %d",
			server.requests(), events)
	}

	// One event per request, in enqueue order: nothing was dropped, reordered, or
	// duplicated by the split (Req 5.8, 5.14).
	assertDeliveryBatches(t, server, want)

	for i, size := range server.bodySizes() {
		if size > maxBatchBodyBytes {
			t.Errorf("request %d body was %d bytes, want at most %d (Req 5.14)", i, size, maxBatchBodyBytes)
		}
	}

	if !waitFor(func() bool { return s.Snapshot().Delivered == events }, deliveryWaitTimeout) {
		t.Fatalf("Delivered = %d, want %d", s.Snapshot().Delivered, events)
	}
	snap := s.Snapshot()
	if snap.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0: an event that does not fit is retained, never discarded (Req 5.14)", snap.Dropped)
	}
	assertCounterIdentities(t, "after the body cap split three batches", snap)
}

// TestDeliveryFitsKeepsBodiesUnderTheCap drives fits directly at the boundary,
// which no end-to-end test can do without multi-megabyte payloads.
//
// The pending batch is described by its count and running byte total, exactly as
// run maintains them, so the arithmetic under test is the arithmetic that runs in
// production.
func TestDeliveryFitsKeepsBodiesUnderTheCap(t *testing.T) {
	var s Shipper

	// An event sized so that exactly three of them fill a body to the byte: the
	// cap minus the separators of a three-event body ('[', ',', ',', ']'),
	// divided three ways.
	const third = (maxBatchBodyBytes - 4) / 3
	ev := &Event{}
	ev.SetSerializedLen(third)

	cases := []struct {
		name         string
		pendingCount int
		pendingBytes int
		want         bool
	}{
		{name: "an empty batch always fits", pendingCount: 0, pendingBytes: 0, want: true},
		{name: "one third plus one third fits", pendingCount: 1, pendingBytes: third, want: true},
		{name: "two thirds plus one third fills the body exactly", pendingCount: 2, pendingBytes: 2 * third, want: true},
		{name: "three thirds plus one third does not", pendingCount: 3, pendingBytes: 3 * third, want: false},
		{
			name:         "exactly at the cap fits",
			pendingCount: 1,
			pendingBytes: maxBatchBodyBytes - third - 3, // '[' + ',' + ']'
			want:         true,
		},
		{
			name:         "one byte over the cap does not",
			pendingCount: 1,
			pendingBytes: maxBatchBodyBytes - third - 2,
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pending := make([]*Event, tc.pendingCount)
			for i := range pending {
				pending[i] = ev
			}
			if got := s.fits(pending, tc.pendingBytes, ev); got != tc.want {
				t.Errorf("fits(%d events, %d bytes, +%d bytes) = %t, want %t (body would be %d, cap %d)",
					tc.pendingCount, tc.pendingBytes, third, got, tc.want,
					batchBodyBytes(tc.pendingCount+1, tc.pendingBytes+third), maxBatchBodyBytes)
			}
		})
	}

	// An event alone larger than the whole cap still forms a batch: a batch
	// always holds at least one event, or it could never make progress.
	huge := &Event{}
	huge.SetSerializedLen(maxBatchBodyBytes * 2)
	if !s.fits(nil, 0, huge) {
		t.Error("fits(empty, oversized event) = false, want true: a batch must always hold at least one event")
	}
}

// TestDeliveryBatchBodyBytesMatchesTheMarshalledBody pins the separator
// arithmetic fits depends on against the bytes IngestClient.Send actually
// produces. If json.Marshal's array encoding and batchBodyBytes ever disagree,
// the cap becomes an estimate rather than a bound, and this is where that shows.
func TestDeliveryBatchBodyBytesMatchesTheMarshalledBody(t *testing.T) {
	events := []*Event{
		deliveryPaddedEvent(t, "arith-0", 16),
		deliveryPaddedEvent(t, "arith-1", 32),
		deliveryPaddedEvent(t, "arith-2", 64),
	}

	total := 0
	for count := 1; count <= len(events); count++ {
		total += eventBytes(events[count-1])

		body, err := json.Marshal(events[:count])
		if err != nil {
			t.Fatalf("marshalling a batch of %d returned error: %v", count, err)
		}
		if got, want := batchBodyBytes(count, total), len(body); got != want {
			t.Errorf("batchBodyBytes(%d, %d) = %d, want the marshalled length %d", count, total, got, want)
		}
	}

	if got, want := batchBodyBytes(0, 0), len("[]"); got != want {
		t.Errorf("batchBodyBytes(0, 0) = %d, want %d", got, want)
	}
}

// TestDeliveryEventBytesTreatsAZeroCachedLengthAsACacheMiss covers the documented
// contract of Event.SerializedLen: 0 means "unknown", not "empty".
//
// Trusting it would let an unbounded number of unmeasured events into one body,
// turning the Req 5.14 cap into a silent 413 from the provider.
func TestDeliveryEventBytesTreatsAZeroCachedLengthAsACacheMiss(t *testing.T) {
	// Straight from conversion, with no capEventSize call, so nothing is cached.
	ev := convertForTest(t, newTestRecord(slog.LevelError, "cache miss"))
	if got := ev.SerializedLen(); got != 0 {
		t.Fatalf("SerializedLen = %d on a freshly converted event, want 0: this test needs an uncached event", got)
	}

	measured, ok := serializedSize(ev)
	if !ok {
		t.Fatal("a converted event does not marshal")
	}
	if got := eventBytes(ev); got != measured {
		t.Errorf("eventBytes on a cache miss = %d, want the measured length %d", got, measured)
	}
	// The measurement is cached, so the check stays O(1) for the second call the
	// loop makes on the same event.
	if got := ev.SerializedLen(); got != measured {
		t.Errorf("SerializedLen = %d after eventBytes measured the event, want the cached %d", got, measured)
	}

	if got := eventBytes(nil); got != 0 {
		t.Errorf("eventBytes(nil) = %d, want 0", got)
	}
}
