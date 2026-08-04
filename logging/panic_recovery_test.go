package logging

// Tests for delivery-goroutine panic recovery and the 5-panic disable rule of
// logging/shipper.go: run, deliverOnce, recoverDeliveryPanic,
// discardPanickedBatch, and disableAfterDeliveryPanics (Req 8.3, 8.4).
//
// **How the delivery goroutine is made to panic, and why this way.** The
// mechanism is a Log_Event whose `context` object holds a value whose
// MarshalJSON panics. IngestClient.Send marshals the whole Delivery_Batch as its
// first act, encoding/json re-panics anything that is not its own internal error
// sentinel, and the panic therefore surfaces inside transmit, on the delivery
// goroutine, exactly where a real defect in the delivery path would.
//
// Three alternatives were rejected:
//
//   - **A panicking http.RoundTripper injected into s.client.http.** It works —
//     net/http's client makes no attempt to recover — but a panic through
//     http.Client.Do skips the stopTimer of net/http's own send path, leaving a
//     timer goroutine alive for the client's 10 s timeout. This package has
//     tests that assert exact goroutine counts, so one test leaking five
//     goroutines that die a few seconds later is a flake generator for whichever
//     test happens to be running then.
//   - **A nil collaborator.** transmit's cannotTransmit guard catches a nil
//     client deliberately, and a nil counters or clock would panic in a place no
//     production defect could reach.
//   - **A production seam.** Nothing here needs one: the panic is injected
//     entirely through data that the delivery path already handles.
//
// So the injection costs zero production code, opens no connection, and leaves no
// goroutine behind: the panic is raised and recovered before a single byte is
// written to the endpoint, which is also why the request assertions below expect
// *zero* requests from a panicking batch.
//
// The harness is the sd* one from logging/suspend_disable_test.go — the scripted
// endpoint, the mutex-guarded diagnostic writer, the fake clock, and the Shipper
// bound through a fan-out handler — because the disciplines it enforces are the
// ones these tests need too: no wall-clock sleeping for time to pass, loopback
// only (Req 12.3), and the accounting identity asserted at every checkpoint
// (Req 11.4). Everything declared here is prefixed pr/PR so it cannot collide
// with that file or with logging/delivery_test.go.

import (
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// prPanicPrefix is the head of every injected panic value: distinctive, so a
// diagnostic can be attributed to this file, and short enough to survive the
// 1024-character truncation of Req 8.3 intact.
const prPanicPrefix = "panic-recovery-test: the event marshaller exploded"

// prPanicPadding pads the panic value well past maxDiagPanicChars, so that the
// truncation is observable rather than assumed.
const prPanicPadding = 4096

// prCachedEventBytes is the serialized length cached on every injected event.
//
// It matters that it is non-zero: eventBytes treats a zero as a cache miss and
// marshals the event to measure it, which for these events would panic in the
// queue arm rather than in transmit. A cached length keeps the panic where the
// test intends it — inside Send, with a full Delivery_Batch in flight — and the
// value itself is arbitrary as long as it is far below the 1 MiB body cap so the
// cap trigger of Req 5.14 plays no part.
const prCachedEventBytes = 256

// prPanicValue is the panic value every injected panic carries.
func prPanicValue() string {
	return prPanicPrefix + strings.Repeat("x", prPanicPadding)
}

// prBoom is a JSON value that panics instead of marshalling.
//
// encoding/json calls MarshalJSON while encoding the Delivery_Batch and re-panics
// anything that is not its own internal error type, so this is what turns a
// transmission into a delivery-goroutine panic.
type prBoom struct{}

func (prBoom) MarshalJSON() ([]byte, error) { panic(prPanicValue()) }

// prPanickingEvent builds one Log_Event that cannot be marshalled without
// panicking.
//
// Every required top-level field is populated so the event is otherwise exactly
// what convert produces (Req 3.1); only the optional context object is hostile.
func prPanickingEvent() *Event {
	ev := &Event{
		DT:          fakeClockEpoch.Format(time.RFC3339Nano),
		Level:       "ERROR",
		Message:     "panic-recovery-test event",
		Service:     defaultServiceName,
		Environment: productionEnvironment,
		Context:     map[string]any{"boom": prBoom{}},
	}
	ev.SetSerializedLen(prCachedEventBytes)
	return ev
}

// prEmitPanicking admits n panicking events into the Event_Queue.
//
// The admission is counted first, which is what Handle does before conversion, so
// the counters see exactly what a real ERROR record would have produced and the
// Req 11.4 identity is meaningful throughout. The handler cannot be used here
// because convert produces a well-formed event by construction; the queue is the
// nearest seam to the delivery goroutine that takes an Event as data.
func prEmitPanicking(sd *sdShipper, n int) {
	for i := 0; i < n; i++ {
		sd.counters.addAdmitted(1)
		sd.enqueue(prPanickingEvent())
	}
}

// prAssertPanicDiagnostic checks one record against everything Req 8.3 requires
// of the diagnostic a recovered delivery panic writes: ERROR level, the panic
// value truncated to 1024 characters, and the stack truncated to 8192 bytes.
func prAssertPanicDiagnostic(t *testing.T, rec map[string]any) {
	t.Helper()

	if rec["level"] != "ERROR" {
		t.Errorf("the delivery-panic diagnostic level = %v, want ERROR (Req 8.3)", rec["level"])
	}
	if message, _ := rec["msg"].(string); message != deliveryPanicMessage {
		t.Errorf("the delivery-panic diagnostic message = %q, want %q", message, deliveryPanicMessage)
	}

	panicValue, ok := rec["panic"].(string)
	if !ok {
		t.Fatalf("the delivery-panic diagnostic carries no panic value, which Req 8.3 requires: %v", rec)
	}
	if !strings.HasPrefix(panicValue, prPanicPrefix) {
		t.Errorf("the reported panic value = %q..., want it to start with %q", prTrim(panicValue), prPanicPrefix)
	}
	// Exactly at the cap, not merely under it: the injected value is 4096
	// characters longer than the limit, so anything else means the truncation of
	// Req 8.3 did not happen.
	if got := utf8.RuneCountInString(panicValue); got != maxDiagPanicChars {
		t.Errorf("the reported panic value is %d characters, want it truncated to exactly %d (Req 8.3)",
			got, maxDiagPanicChars)
	}

	stack, ok := rec["stack"].(string)
	if !ok || stack == "" {
		t.Fatalf("the delivery-panic diagnostic carries no stack trace, which Req 8.3 requires: %v", rec)
	}
	if len(stack) > maxDiagStackBytes {
		t.Errorf("the reported stack is %d bytes, want at most %d (Req 8.3)", len(stack), maxDiagStackBytes)
	}
	// The stack must describe the goroutine that panicked and not merely the
	// frame that recovered it, which is why the capture happens inside the
	// recovery. MarshalJSON is the frame that panicked.
	if !strings.Contains(stack, "MarshalJSON") {
		t.Errorf("the reported stack does not name the frame that panicked (Req 8.3): %q", prTrim(stack))
	}
}

// prTrim shortens a value for a failure message, so a 1024-character panic value
// or an 8 KiB stack does not bury the assertion that failed.
func prTrim(s string) string {
	const limit = 120
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// ---------------------------------------------------------------------------
// One recovered panic (Req 8.3)
// ---------------------------------------------------------------------------

// TestPRDeliveryPanicDiscardsTheBatchAndTheLoopKeepsWorking covers Req 8.3 end to
// end: the panic is recovered rather than propagated, the in-flight Delivery_Batch
// is discarded and counted under reasonAbandonedBatch, its in-flight count is
// released exactly once, exactly one ERROR carries the truncated panic value and
// stack, and the delivery loop transmits the next batch — within 1 second, on the
// same goroutine, without the Shipper leaving enabled state.
//
// The sentinel event is the exactly-once check. One admitted event is held in
// flight for the whole test, standing in for an event of some other batch, so a
// second release of the panicking batch would show up as an InFlight of 0 instead
// of being hidden by the counter's clamp at zero.
func TestPRDeliveryPanicDiscardsTheBatchAndTheLoopKeepsWorking(t *testing.T) {
	server := newSDServer(t, 202)
	sd := newSDShipper(t, server)

	sd.counters.addAdmitted(1)
	sd.counters.addInFlight(1)

	// The batch-size trigger transmits without any interval elapsing (Req 5.5),
	// and the transmission panics while marshalling the body.
	prEmitPanicking(sd, sdBatchSize)

	// Exactly one Self_Diagnostic, at ERROR, with the truncated panic and stack.
	// only() also asserts the logshipper marker of Req 8.1 and that no
	// Source_Token leaked (Req 10.4).
	prAssertPanicDiagnostic(t, sd.diag.only(t))

	// The batch is discarded and counted (Req 8.3).
	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped == sdBatchSize })
	if !ok {
		t.Fatalf("Dropped = %d after the recovered panic, want the %d events of the discarded batch (%+v)",
			snap.Dropped, sdBatchSize, snap)
	}
	if got := snap.DroppedByReason[reasonAbandonedBatch]; got != sdBatchSize {
		t.Errorf("DroppedByReason[reasonAbandonedBatch] = %d, want %d: a panicked batch is an abandoned batch (Req 8.3)",
			got, sdBatchSize)
	}
	if snap.InFlight != 1 {
		t.Errorf("InFlight = %d, want 1 (the sentinel): the panicked batch's in-flight release must happen exactly once (Req 11.3, 11.4)",
			snap.InFlight)
	}
	if snap.Delivered != 0 {
		t.Errorf("Delivered = %d, want 0: a batch that never reached the endpoint is not delivered", snap.Delivered)
	}
	if snap.AbandonedBatches != 0 {
		t.Errorf("AbandonedBatches = %d, want 0: that counter is attempt exhaustion (Req 7.7), which a panic is not",
			snap.AbandonedBatches)
	}
	// Req 8.2: the ERROR describing the panic went to the diagnostic writer and
	// not into the Event_Queue, so the only admissions are the ones this test
	// made — the sentinel plus the panicking batch.
	if snap.Enqueued != sdBatchSize+1 {
		t.Errorf("Enqueued = %d, want %d: reporting a delivery panic must enqueue nothing (Req 8.2)",
			snap.Enqueued, sdBatchSize+1)
	}
	assertCounterIdentities(t, "after one recovered delivery panic", snap)
	server.assertRequests(t, 0, "the panic happens while marshalling, before any request is issued")
	sd.assertState(t, stateEnabled, "one panic is recovered, not fatal")

	// Req 8.3: delivery of subsequent Delivery_Batches resumes within 1 second.
	// The wait is bounded at exactly that, so a loop that needed a restart, a
	// fresh ticker, or a backoff would fail here rather than pass slowly.
	sd.emit(sdBatchSize)
	if !waitFor(func() bool { return sd.Snapshot().Delivered == sdBatchSize }, time.Second) {
		t.Fatalf("Delivered = %d one second after the recovered panic, want %d: the delivery loop must resume within 1 second (Req 8.3) (%+v)",
			sd.Snapshot().Delivered, sdBatchSize, sd.Snapshot())
	}
	if !server.waitRequests(1) {
		t.Fatalf("the batch that followed the panic never reached the endpoint (Req 8.3)")
	}

	snap = sd.Snapshot()
	if snap.InFlight != 1 || snap.Dropped != sdBatchSize {
		t.Errorf("InFlight = %d, Dropped = %d after the following batch was delivered, want 1 (the sentinel) and %d (%+v)",
			snap.InFlight, snap.Dropped, sdBatchSize, snap)
	}
	assertCounterIdentities(t, "after the loop resumed and delivered the next batch", snap)
	server.assertRequests(t, 1, "one panicking batch that issued no request, one delivered batch")

	// Still exactly one diagnostic: the recovery writes one ERROR, and a
	// successful delivery is silent (Req 7.1).
	if got := len(sd.diag.lines()); got != 1 {
		t.Errorf("wrote %d Self_Diagnostics for one recovered panic and one delivery, want exactly 1 (Req 8.3): %q",
			got, sd.diag.text())
	}
}

// ---------------------------------------------------------------------------
// The 5-panic ceiling (Req 8.4)
// ---------------------------------------------------------------------------

// TestPRFifthDeliveryPanicDisablesDeliveryPermanently covers Req 8.4: the first
// four panics are recovered and delivery continues, the 5th disables the Shipper
// for the remainder of the process lifetime, zero replacement goroutines are
// started — the delivery goroutine is gone, not restarted — and exactly one
// further ERROR states that delivery is permanently stopped.
//
// It also pins the two consequences an operator would notice next: nothing is
// transmitted afterwards, at a flush boundary or otherwise (Req 7.9), and later
// admissions are discarded at enqueue under reasonNonRetryable (Req 8.9).
func TestPRFifthDeliveryPanicDisablesDeliveryPermanently(t *testing.T) {
	server := newSDServer(t, 202)

	// Captured after the endpoint is running and before the delivery goroutine
	// exists, so the delivery goroutine is the only expected difference from it.
	baseline := stableGoroutineCount()
	sd := newSDShipper(t, server)

	for panics := 1; panics <= maxDeliveryPanics; panics++ {
		prEmitPanicking(sd, sdBatchSize)

		want := uint64(panics * sdBatchSize)
		snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped >= want })
		if !ok {
			t.Fatalf("Dropped = %d after panic %d, want %d: every recovered panic discards its batch (Req 8.3) (%+v)",
				snap.Dropped, panics, want, snap)
		}
		if snap.DroppedByReason[reasonAbandonedBatch] != want {
			t.Fatalf("DroppedByReason[reasonAbandonedBatch] = %d after panic %d, want %d (Req 8.3)",
				snap.DroppedByReason[reasonAbandonedBatch], panics, want)
		}
		if snap.InFlight != 0 {
			t.Fatalf("InFlight = %d after panic %d, want 0: each discarded batch releases its events exactly once (Req 11.3)",
				snap.InFlight, panics)
		}
		assertCounterIdentities(t, "after recovered panic", snap)

		if panics < maxDeliveryPanics {
			// Below the ceiling the Shipper keeps running: still enabled, still
			// one delivery goroutine, and no disable diagnostic yet.
			sd.assertState(t, stateEnabled, "below the 5-panic ceiling")
			if got := len(sd.diag.lines()); got != panics {
				t.Fatalf("wrote %d Self_Diagnostics after %d recovered panics, want exactly one each (Req 8.3): %q",
					got, panics, sd.diag.text())
			}
		}
	}

	// Req 8.4: permanently disabled on the 5th.
	if !sd.waitState(stateDisabled) {
		t.Fatalf("state = %s after %d recovered panics, want disabled (Req 8.4)",
			sdStateName(sd.state.Load()), maxDeliveryPanics)
	}

	// Req 8.4: zero replacement delivery goroutines. The recovery never started
	// one — it re-entered the select on the same goroutine — and the 5th panic
	// ends that goroutine, so the count returns to the pre-Start baseline.
	if !waitFor(func() bool { return runtime.NumGoroutine() <= baseline }, sdWaitTimeout) {
		t.Errorf("goroutine count = %d after the 5th panic, want at most the pre-Start baseline %d: delivery must stop, and no replacement goroutine may be started (Req 8.4)",
			runtime.NumGoroutine(), baseline)
	}

	// One ERROR per recovered panic, plus exactly one stating that delivery is
	// permanently stopped.
	const wantRecords = maxDeliveryPanics + 1
	if !sd.diag.waitRecords(wantRecords) {
		t.Fatalf("wrote %d Self_Diagnostics, want %d (%d panics plus the permanent-stop ERROR of Req 8.4): %q",
			len(sd.diag.lines()), wantRecords, maxDeliveryPanics, sd.diag.text())
	}
	time.Sleep(sdSettle) // a further diagnostic would be a requirement violation
	records := sd.diag.records(t)
	if len(records) != wantRecords {
		t.Fatalf("wrote %d Self_Diagnostics, want exactly %d (Req 8.3, 8.4): %q",
			len(records), wantRecords, sd.diag.text())
	}
	for i, rec := range records[:maxDeliveryPanics] {
		if rec["logshipper"] != true {
			t.Errorf("panic diagnostic %d is missing the logshipper marker attribute (Req 8.1): %v", i+1, rec)
		}
		prAssertPanicDiagnostic(t, rec)
	}

	final := records[wantRecords-1]
	if final["level"] != "ERROR" {
		t.Errorf("the permanent-stop diagnostic level = %v, want ERROR (Req 8.4)", final["level"])
	}
	message, _ := final["msg"].(string)
	lowered := strings.ToLower(message)
	if !strings.Contains(lowered, "permanently stopped") || !strings.Contains(lowered, "panic") {
		t.Errorf("the permanent-stop diagnostic message = %q, want it to state that log delivery is permanently stopped after repeated panics (Req 8.4)",
			message)
	}
	assertDiagField(t, final, "panics", maxDeliveryPanics, "Req 8.4")
	if strings.Contains(sd.diag.text(), sdToken) {
		t.Errorf("a diagnostic leaked the Source_Token (Req 10.4): %q", sd.diag.text())
	}

	// Req 7.9: zero HTTP requests from a disabled Shipper, however many flush
	// boundaries pass. Nothing was ever transmitted here, because every panic
	// happened before its request was issued.
	for i := 0; i < 3; i++ {
		sd.clock.Advance(sdFlushEvery)
	}
	time.Sleep(sdSettle)
	server.assertRequests(t, 0, "a disabled Shipper transmits nothing (Req 7.9)")

	// Req 8.9: later admissions are discarded at enqueue and attributed to
	// reasonNonRetryable, and the console child of the fan-out still wrote every
	// record.
	dropped := uint64(maxDeliveryPanics * sdBatchSize)
	sd.emit(sdBatchSize)
	snap, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.Dropped == dropped+sdBatchSize })
	if !ok {
		t.Fatalf("Dropped = %d after admissions to a Shipper disabled by repeated panics, want %d (Req 8.9) (%+v)",
			snap.Dropped, dropped+sdBatchSize, snap)
	}
	if got := snap.DroppedByReason[reasonNonRetryable]; got != sdBatchSize {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d: an enqueue while disabled is non-retryable (Req 8.9)",
			got, sdBatchSize)
	}
	if snap.QueueDepth != 0 || snap.InFlight != 0 || snap.Delivered != 0 {
		t.Errorf("QueueDepth = %d, InFlight = %d, Delivered = %d after the disable, want 0, 0, 0 (%+v)",
			snap.QueueDepth, snap.InFlight, snap.Delivered, snap)
	}
	assertCounterIdentities(t, "after admissions to a Shipper disabled by repeated panics", snap)
	server.assertRequests(t, 0, "an admission to a disabled Shipper transmits nothing (Req 7.9)")

	if got := len(sd.diag.lines()); got != wantRecords {
		t.Errorf("wrote %d Self_Diagnostics in total, want %d: the disable is reported once (Req 8.4): %q",
			got, wantRecords, sd.diag.text())
	}
}

// ---------------------------------------------------------------------------
// The discard accounting, directly (Req 8.3, 11.3, 11.4)
// ---------------------------------------------------------------------------

// prCountingShipper returns a Shipper with counters and a diagnostic writer but
// no delivery goroutine, for testing discardPanickedBatch as a function.
func prCountingShipper(t *testing.T) (*Shipper, *sdDiag) {
	t.Helper()
	diag := &sdDiag{}
	cfg := Config{
		Enabled:     true,
		IngestURL:   "https://in.example.test/v1",
		SourceToken: sdToken,
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		BatchSize:   sdBatchSize,
		FlushEvery:  sdFlushEvery,
		QueueSize:   sdQueueSize,
		DiagWriter:  diag,
		Clock:       newFakeClock(),
		Jitter:      FixedJitter(0),
	}
	return NewShipper(cfg), diag
}

// TestPRDiscardPanickedBatchReleasesThePendingBatchAndTheInboundEvent covers the
// half of Req 8.3 that no end-to-end test can reach deterministically: the event
// that has been received from the Event_Queue and counted in flight but not yet
// appended to the pending batch.
//
// That window is real — the 1 MiB cap check between the two can transmit the
// previous batch, and a transmission is where a delivery panic comes from — and
// missing it would leave an event counted in flight for the remaining lifetime of
// the process, with no terminal state left to release it.
func TestPRDiscardPanickedBatchReleasesThePendingBatchAndTheInboundEvent(t *testing.T) {
	s, _ := prCountingShipper(t)

	// Three admitted events in flight: two batched, one received and counted but
	// not yet appended.
	s.counters.addAdmitted(3)
	s.counters.addInFlight(3)
	st := &deliveryState{
		batchSize:    sdBatchSize,
		pending:      []*Event{prPanickingEvent(), prPanickingEvent()},
		pendingBytes: 2 * prCachedEventBytes,
		inbound:      prPanickingEvent(),
	}

	if got := st.inFlightEvents(); got != 3 {
		t.Fatalf("inFlightEvents() = %d, want 3 (two pending plus one inbound)", got)
	}
	if got := s.discardPanickedBatch(st); got != 3 {
		t.Errorf("discardPanickedBatch() = %d, want 3: the inbound event is part of the discarded batch (Req 8.3)", got)
	}

	snap := s.Snapshot()
	if snap.Dropped != 3 || snap.DroppedByReason[reasonAbandonedBatch] != 3 {
		t.Errorf("Dropped = %d (abandoned %d), want 3 and 3 (Req 8.3)",
			snap.Dropped, snap.DroppedByReason[reasonAbandonedBatch])
	}
	if snap.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0: every held event is released (Req 11.3)", snap.InFlight)
	}
	assertCounterIdentities(t, "after discarding a panicked batch and its inbound event", snap)

	// The state is empty afterwards, so the next iteration starts a fresh batch
	// and nothing can be discarded twice.
	if len(st.pending) != 0 || st.pendingBytes != 0 || st.inbound != nil {
		t.Errorf("state after the discard = %d pending, %d bytes, inbound %v, want empty",
			len(st.pending), st.pendingBytes, st.inbound)
	}
	if got := s.discardPanickedBatch(st); got != 0 {
		t.Errorf("a second discardPanickedBatch() reported %d events, want 0", got)
	}
	assertCounterIdentities(t, "after a second discard of the same state", s.Snapshot())
}

// TestPRDiscardPanickedBatchDoesNotDoubleCountAfterTransmitAccounted covers the
// other side of the same rule: a panic raised *after* transmit reached a terminal
// state for the batch — in the diagnostic write that follows it, in practice —
// must not release or drop those events a second time, because transmit already
// did (Req 11.4, 11.5).
//
// The state is arranged exactly as the delivery goroutine leaves it in that
// window: the batch is still in deliveryState, because run clears pending only
// after transmit returns and transmit has not returned, it has panicked. No
// sentinel is injected, because one would misrepresent the system — Req 5.3 and
// 5.15 allow one Delivery_Batch in flight at a time, so no foreign event can be in
// flight here, and that is the invariant the discard relies on.
//
// A discard that released the batch a second time is still caught without a
// sentinel: the in-flight counter would clamp at zero, but the duplicate drop
// would push Dropped above Enqueued and break the Req 11.4 identity.
func TestPRDiscardPanickedBatchDoesNotDoubleCountAfterTransmitAccounted(t *testing.T) {
	s, _ := prCountingShipper(t)

	batch := []*Event{prPanickingEvent(), prPanickingEvent()}
	s.counters.addAdmitted(uint64(len(batch)))
	s.counters.addInFlight(int64(len(batch)))

	// Exactly what transmit does on a non-retryable terminal outcome: release,
	// then record the drop.
	s.releaseInFlight(len(batch))
	s.counters.addDropped(reasonNonRetryable, uint64(len(batch)))

	st := &deliveryState{batchSize: sdBatchSize, pending: batch, pendingBytes: 2 * prCachedEventBytes}

	if got := s.discardPanickedBatch(st); got != 0 {
		t.Errorf("discardPanickedBatch() = %d after transmit accounted for the batch, want 0: a second release double-counts (Req 11.4)", got)
	}

	snap := s.Snapshot()
	if snap.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0: transmit's release stands and is not repeated (Req 11.3)", snap.InFlight)
	}
	if snap.Dropped != uint64(len(batch)) || snap.DroppedByReason[reasonAbandonedBatch] != 0 {
		t.Errorf("Dropped = %d (abandoned %d), want %d and 0: the drop transmit recorded must not be duplicated (Req 11.5)",
			snap.Dropped, snap.DroppedByReason[reasonAbandonedBatch], len(batch))
	}
	assertCounterIdentities(t, "after a panic that followed transmit's own accounting", snap)
}

// TestPRContainPanicAbsorbsAPanicFromTheReportingPath covers the guard that keeps
// a broken diagnostic writer from turning a contained delivery panic into a dead
// process: the recovery runs inside a deferred function that is already recovering
// one, so a second escaping panic would have nothing above it to recover.
func TestPRContainPanicAbsorbsAPanicFromTheReportingPath(t *testing.T) {
	ran := false
	containPanic(func() {
		ran = true
		panic("the diagnostic writer exploded")
	})
	if !ran {
		t.Fatal("containPanic did not run the function it was given")
	}

	// And it is transparent for a function that does not panic.
	completed := false
	containPanic(func() { completed = true })
	if !completed {
		t.Error("containPanic did not run a non-panicking function to completion")
	}
}
