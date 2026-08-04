package logging

// The Log_Shipper: the component that owns the Event_Queue, the delivery
// goroutine, the counters, the Redactor, and the ingest client.
//
// This file holds two things: the parts of the Shipper that producers touch —
// construction, Start, Handler, Snapshot, and enqueue — and the delivery
// goroutine itself, run, with the batching state that is private to it.
//
// The producer half uses only short in-memory critical sections or is called
// once at startup, so it can be read without reasoning about network progress.
// The delivery half is the opposite: every value it batches lives in local
// variables of run, which is why no delivery-state lock is needed for Req 5.15.
//
// The delivery outcome pipeline is now wired end to end here: transmit calls
// classify (logging/classify.go) for the meaning of an attempt, the batchAttempt
// state and backoff of logging/retry.go for retransmission, and the suspension
// and permanent-disable transitions below for 402 and 403.
//
// Close and its final drain live in logging/close.go. Flush lives in
// logging/flush.go; both hand batching work to this delivery loop.
//
// Self_Diagnostics live in logging/diag.go, including the logger this file
// constructs into s.diag and the rate-limited drop WARN enqueue triggers. They
// are separate because the rule that governs them — bound to a writer, never
// reachable from the fan-out handler (Req 8.1, 8.2) — is easier to audit in one
// file than scattered across the call sites that emit them.
//
// The one thing this file does own completely is the guarantee a request
// goroutine depends on: enqueue performs no network I/O and never waits on a
// lock held across I/O. Its only lock protects a closed check and one
// non-blocking channel send, so ingest reachability cannot show up in request
// latency (Req 5.2, 5.13, 6.3, 8.7, 9.4).
//
// The single exception is deliberate and bounded: on the drop path, at most once
// per 60 s, maybeDiagDrop writes one line to the diagnostic writer, which
// Req 6.4 requires. Nothing on the enqueue path ever waits for the network.

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Delivery_Batch body sizing (Req 5.14).
const (
	// maxBatchBodyBytes is the ceiling on the serialized request body of one
	// Delivery_Batch. An event that would push the body above it is held back
	// for the next batch rather than dropped.
	maxBatchBodyBytes = 1048576

	// emptyBatchBodyBytes is len("[]"): the body a batch holding no event would
	// produce. It exists so batchBodyBytes has an honest answer for a count of
	// zero, which no transmitted batch ever has.
	emptyBatchBodyBytes = 2
)

// Delivery-goroutine panic recovery (Req 8.3, 8.4).
const (
	// maxDeliveryPanics is the number of recovered delivery-goroutine panics
	// that permanently disables log delivery: the 5th one stops delivery for the
	// remainder of the process lifetime (Req 8.4).
	//
	// Five rather than "keep going forever" because a delivery path that panics
	// repeatedly is a code defect, not a transient failure, and retrying it
	// without bound would burn CPU and flood the diagnostic writer with one
	// ERROR and one stack trace per attempt.
	maxDeliveryPanics = 5

	// deliveryPanicMessage is the ERROR each recovered panic writes (Req 8.3).
	// It carries no status, no endpoint, and no token: the panic value and the
	// stack are what the requirement asks for, and diagPanic supplies both,
	// truncated.
	deliveryPanicMessage = "betterstack log delivery goroutine panicked: delivery batch discarded"

	// deliveryPanicDisabledMessage is the single ERROR written when the panic
	// ceiling is reached, stating that delivery is permanently stopped
	// (Req 8.4).
	deliveryPanicDisabledMessage = "betterstack log delivery permanently stopped after repeated delivery goroutine panics"
)

// suspensionWindow is how long HTTP 402 suspends transmission for (Req 7.10).
//
// It is measured on the injected Clock, never on wall-clock time, so a test
// drives the whole window by advancing a fake clock (Req 12.2).
const suspensionWindow = 15 * time.Minute

// Delivery state, held in Shipper.state as an atomic.Int32 because it is written
// by the delivery goroutine (403 disables, 402 suspends) and read by every
// producer on the enqueue path.
//
// stateEnabled is 0 on purpose, so that the zero value of atomic.Int32 — and
// therefore of a Shipper composed literally, which the handler tests do — is a
// Shipper that accepts events. A disabled zero value would make every
// directly-constructed Shipper silently drop.
const (
	// stateEnabled accepts events and transmits Delivery_Batches (Req 1.7).
	stateEnabled int32 = iota
	// stateDisabled accepts nothing and transmits nothing for the remaining
	// lifetime of the process (Req 1.8, 1.11, 7.8, 7.9, 8.4, 8.9).
	stateDisabled
	// stateSuspended keeps accepting events into the Event_Queue while
	// transmitting nothing, for the 15 minutes that follow HTTP 402
	// (Req 7.10, 7.12). suspendTransmission enters it and
	// resumeIfSuspensionElapsed leaves it, at a flush boundary.
	stateSuspended
)

// FlushResult reports the outcome of a synchronous Flush (Req 12.4).
//
// Drained distinguishes "the queue reached empty" from "the deadline elapsed
// first", which a delivered/dropped pair alone cannot express: a flush that
// timed out with nothing left to send and a flush that drained fully report the
// same counts.
//
// The type lives here because flushRequest, which the Shipper struct embeds a
// channel of, carries it.
type FlushResult struct {
	Delivered uint64
	Dropped   uint64
	Drained   bool
}

// CloseResult reports the outcome of the close operation. It is stored on the
// Shipper so every concurrent Close caller receives the same published value.
type CloseResult struct {
	Flushed   uint64
	Discarded uint64
	TimedOut  bool
}

// flushRequest is one Flush call, addressed to the delivery goroutine.
//
// It carries a reply channel rather than a shared result field because the
// delivery goroutine is the only writer of the counts it reports: handing them
// back over a channel gives the happens-before edge for free, with no lock and no
// second synchronization rule to remember.
//
// Flush owns the send side and the deadline semantics (1 ms to 60 s); the
// delivery loop owns the receive arm.
type flushRequest struct {
	deadline time.Duration
	reply    chan FlushResult
}

// Shipper owns the Event_Queue, the delivery goroutine, and the counters.
//
// Field ownership, straight from the design's state-ownership table, because it
// is what makes the rest of the package's absence of locks legible:
//
//   - cfg, client, redactor, clock, jitter, diag: immutable after construction,
//     read without synchronization.
//   - queue: a buffered channel. Producers use a non-blocking send; the delivery
//     goroutine is the only receiver.
//   - counters: guarded by its own mutex; lifecycleMu separately covers only
//     Start/Close admission transitions, and neither lock is held across I/O.
//   - state, closed: atomics, written by the delivery goroutine and by Close,
//     read by producers.
//   - dropDiag: an atomic timestamp used on the queue-full path.
//   - closeOnce, closeDone, closeFinished: exactly-once execution plus start and
//     completion broadcasts.
//   - pending batch, attempt count and backoff deadline: private to the delivery
//     goroutine and therefore not fields here at all.
//   - suspendUntil: the one piece of delivery-goroutine-private state that is a
//     field, and it is unsynchronised on purpose. See the field.
//
// There is no package-level mutable state, so independent Shippers constructed by
// parallel tests cannot interfere (Req 12.7).
type Shipper struct {
	cfg      Config
	client   *IngestClient
	redactor *Redactor
	clock    Clock
	jitter   Jitter

	// queue is the Event_Queue, with capacity cfg.QueueSize. A buffered channel
	// is what bounds occupancy at exactly cap at every instant, including under
	// concurrent sends, with no lock (Req 6.2).
	queue chan *Event

	counters *counters
	diag     *slog.Logger

	// dropDiag gates the queue-full drop diagnostic to one per 60 s (Req 6.4,
	// 6.8). Its whole state is one atomic int64, so the queue-full diagnostic
	// gate needs no additional lock, and its zero value means "nothing written
	// yet", so a Shipper composed literally is as usable as one from NewShipper.
	// logging/diag.go owns every read and write of it.
	dropDiag dropDiagLimiter

	state atomic.Int32 // stateEnabled | stateDisabled | stateSuspended

	// suspendUntil is the clock time at which a 402 suspension expires
	// (Req 7.10). It carries no mutex and no atomic, because it is written and
	// read by the delivery goroutine alone: suspendTransmission sets it from
	// transmit, and resumeIfSuspensionElapsed reads it from run's flush-tick
	// arm, both on that one goroutine.
	//
	// The pair is deliberately asymmetric. `state` must be an atomic because
	// every producer reads it on the enqueue path, but the *deadline* is
	// nobody's business but the delivery goroutine's, and keeping it plain is
	// what the design's state-ownership table asks for. Nothing outside that
	// goroutine may ever read this field; doing so would be a data race that
	// -race would report (Req 11.12).
	suspendUntil time.Time

	// lifecycleMu serializes Start and the final close transition, and makes
	// enqueue's closed check plus channel send indivisible with finalDrain's
	// empty-queue seal. It is never held across HTTP, clock, or diagnostic I/O.
	lifecycleMu sync.Mutex
	startOnce   sync.Once
	started     atomic.Bool

	closeCtx      context.Context
	closeCancel   context.CancelFunc
	closeDeadline time.Time // published before closeDone is closed; read by delivery goroutine

	closeOnce     sync.Once
	closeDone     chan struct{}
	closeFinished chan struct{}
	closeResult   CloseResult
	closed        atomic.Bool

	flushReq chan flushRequest
}

// NewShipper builds a Shipper from an already-validated Config.
//
// It starts no goroutine and opens no connection: Start is what does that, and
// only when the configuration resolved to enabled. Splitting construction from
// starting is what lets main.go emit the startup diagnostics between the two, and
// lets a test build a Shipper, drive the enqueue path, and never run a delivery
// loop at all.
//
// It reads no environment variable (Req 1.14, 12.1). Every value comes from cfg,
// which main.go obtains from ResolveConfig over strings the configs package read.
//
// The IngestClient is constructed even for a disabled Shipper. Constructing one
// is not connecting: an http.Client opens nothing until a request is made, and a
// disabled Shipper never reaches Send, so Req 1.11 and Req 12.3's "zero outbound
// connections" still hold. Constructing it unconditionally keeps the field
// non-nil, so no later code path needs a nil check that would only ever matter
// after a bug.
func NewShipper(cfg Config) *Shipper {
	// applyDefaults fills only the injectable collaborators that were left nil —
	// DiagWriter, Clock, Jitter — and reads no environment variable. Calling it
	// here means a Config assembled by hand, as tests do, is as usable as one
	// returned by ResolveConfig, which has already applied the same defaults.
	cfg = applyDefaults(cfg)

	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		// ResolveConfig clamps QueueSize to 1..100000, so this is reachable only
		// from a hand-assembled Config. make would panic on a negative capacity,
		// and a zero capacity would make every enqueue drop, so the documented
		// default is applied instead of failing or silently shipping nothing.
		queueSize = defaultQueueSize
	}

	deliveryCtx, cancelDelivery := context.WithCancel(context.Background())
	s := &Shipper{
		cfg:           cfg,
		client:        NewIngestClient(cfg.IngestURL, cfg.SourceToken, DefaultRequestTimeout),
		redactor:      NewRedactor(cfg.SourceToken),
		clock:         cfg.Clock,
		jitter:        cfg.Jitter,
		queue:         make(chan *Event, queueSize),
		counters:      &counters{},
		diag:          newDiagLogger(cfg),
		closeCtx:      deliveryCtx,
		closeCancel:   cancelDelivery,
		closeDone:     make(chan struct{}),
		closeFinished: make(chan struct{}),
		flushReq:      make(chan flushRequest),
	}
	if !cfg.Enabled {
		s.state.Store(stateDisabled)
	}
	return s
}

// Start starts the single delivery goroutine (Req 5.3).
//
// It is a no-op for a disabled Shipper: zero goroutines and zero HTTP requests
// for the lifetime of the process is exactly what Req 1.11 requires, and it is
// also what makes the empty-token path in tests observable as zero goroutine
// growth (Req 12.3).
//
// Calling it twice starts one goroutine, not two. See the startOnce field.
func (s *Shipper) Start() {
	if s.state.Load() == stateDisabled {
		return // Req 1.11: no goroutine, no connection
	}
	s.startOnce.Do(func() {
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()

		if s.state.Load() == stateDisabled || s.closed.Load() {
			return
		}
		select {
		case <-s.closeDone:
			return // Close won the Start/Close race; no second queue consumer.
		default:
		}
		s.started.Store(true)
		go s.run()
	})
}

// run is the delivery goroutine: the single goroutine that batches Log_Events
// and transmits Delivery_Batches (Req 5.3).
//
// The whole batching state lives in one deliveryState that this frame owns and
// that nothing outside this goroutine ever holds a reference to. That is not a
// stylistic choice, it is what makes the rest of the delivery path lock-free and
// what makes Req 5.15 true by construction: transmit calls Send synchronously,
// so while a transmission is in progress this goroutine is not in the select at
// all, and neither the flush tick nor the batch-size trigger can start a second
// request. A tick that fires during a transmission is coalesced into one pending
// tick by the ticker's one-slot buffer, which is exactly time.Ticker's own
// behaviour, so a long transmission produces one deferred flush rather than a
// backlog of them.
//
// **This function is a wrapper; the select is one call down, in deliverOnce.**
// That shape is what Req 8.3 and Req 8.4 need:
//
//   - deliverOnce recovers a panic from any of its arms and reports it. The
//     recovery cannot resume the select from inside its own deferred function,
//     because by then that frame is unwinding — so it returns here, and the very
//     next statement of this loop re-enters the select. No goroutine is started,
//     no ticker is rebuilt, and nothing sleeps, which is how "resume delivery of
//     subsequent Delivery_Batches within 1 second" (Req 8.3) holds with a margin
//     measured in microseconds rather than by a timer.
//   - The 5-panic counter lives in this frame, which is the one frame that
//     outlives every recovery. See the local below.
//
// The ticker and the health monitor are created here for the same reason: a
// recovered panic must not restart the flush interval or the 5-minute health
// interval, and rebuilding either per iteration would register a new ticker on
// the injected Clock for every event that arrives (Req 12.2).
func (s *Shipper) run() {
	// The ticker comes from the injected Clock, so a test advances a fake clock
	// instead of sleeping for the flush interval (Req 12.2).
	ticker := s.deliveryClock().NewTicker(s.flushInterval())
	defer ticker.Stop()

	// TASK 8.11 added exactly this pair and the health arm in deliverOnce. The
	// 5-minute evaluation cannot ride on the flush ticker, whose interval is
	// configurable up to 300 s, without missing the boundary Req 11.6 bounds at 5
	// seconds. All the policy lives in logging/health.go.
	health := s.newHealthMonitor()
	defer health.stop()

	st := &deliveryState{batchSize: s.batchSize()}

	// panics is the design's panicCount, and it is a local rather than a Shipper
	// field on purpose. The design's state-ownership table makes it
	// delivery-goroutine-private, and this frame is where "private to the
	// delivery goroutine" and "survives a recovery" are both true: a local of
	// deliverOnce would be reset by the very panic it exists to count, and a
	// Shipper field would be shared state with no reader outside this goroutine
	// and would need a synchronization rule to justify.
	//
	// It counts recovered panics for the lifetime of the goroutine, which is the
	// lifetime of the process (Req 8.4): the goroutine is never replaced, so
	// there is no path by which the count restarts at zero.
	panics := 0
	for {
		stop, panicked := s.deliverOnce(ticker, health, st)

		if panicked {
			panics++
			if panics >= maxDeliveryPanics {
				// Req 8.4. Returning is how "start zero replacement delivery
				// goroutines" is implemented: recovery has never started a
				// goroutine — it re-entered the select on this one — and nothing
				// in the package calls `go s.run()` a second time, which
				// startOnce would refuse in any case. The state is already
				// disabled by the time this returns, so every later admission is
				// dropped at enqueue (Req 8.9) and no path can transmit
				// (Req 7.9).
				s.disableAfterDeliveryPanics(panics)
				return
			}
			continue // Req 8.3: straight back into the select
		}
		if stop {
			return
		}
	}
}

// deliveryState is the batching state of the delivery goroutine: the pending
// Delivery_Batch, its running serialized byte total, the one event that has left
// the Event_Queue but not yet joined that batch, and whether the close arm has
// been entered.
//
// These were plain locals of run before panic recovery. They are a struct now
// because the recovery has to reach them: a deferred function in deliverOnce must
// discard exactly the batch that was in flight when the panic happened and clear
// it so the next iteration starts empty, and one pointer shared between run and
// each iteration is what gives both frames the same view of it.
//
// It is still private to the delivery goroutine — run allocates it, only
// deliverOnce and the recovery touch it, and no other goroutine holds a
// reference — so no field here needs synchronization (Req 5.15).
type deliveryState struct {
	// batchSize is the Delivery_Batch size trigger, resolved once at startup so
	// that a hand-assembled Config's non-positive value cannot turn one request
	// per batch into one request per event (Req 5.4).
	batchSize int

	// pending is the batch being accumulated. It never exceeds batchSize, because
	// the only append is immediately followed by the length check that empties
	// it. With at most one batch in flight and the per-event 64 KiB cap of
	// Req 3.12, the retained payload is bounded by
	// (QueueSize + BatchSize) x 65536 bytes — Req 6.6.
	pending []*Event

	// pendingBytes is the sum of the serialized lengths of the events in
	// pending, maintained incrementally so the cap check stays O(1) per event. It
	// is not the body size; batchBodyBytes adds the brackets and separators.
	pendingBytes int

	// inbound is an event that has been received from the Event_Queue and counted
	// in flight (Req 11.3) but has not yet been appended to pending.
	//
	// It exists for the accounting of Req 8.3 and for nothing else. The window it
	// covers is short but real: between the in-flight increment and the append
	// sits the 1 MiB cap check, which can transmit the *previous* batch — a
	// synchronous HTTP request, with retries, which is exactly where a delivery
	// panic is most likely to originate. Without this field a recovery would
	// release len(pending) and leave the received event counted in flight for the
	// remaining lifetime of the process, with no terminal state left to clear it.
	inbound *Event

	// draining records that the closeDone arm has been entered, so a panic raised
	// by the final drain ends the loop instead of resuming it. closeDone stays
	// readable forever once closed, so resuming would re-enter an arm that has
	// just demonstrated that it panics, on every iteration, until the 5-panic
	// ceiling stopped it.
	draining bool
}

// admit moves ev out of inbound and into the pending batch.
//
// inbound is cleared *first*, so that an event which has been appended is counted
// exactly once by inFlightEvents even if the size accounting below panics:
// eventBytes marshals an event whose serialized length was never cached, and a
// panic there must not let a recovery count the same event twice.
func (st *deliveryState) admit(ev *Event) {
	st.inbound = nil
	st.pending = append(st.pending, ev)
	st.pendingBytes += eventBytes(ev)
}

// resetBatch empties the pending batch after it has been handed to transmit or
// discarded. It deliberately leaves inbound alone, which is nil at every call
// site by construction: both transmit triggers run with the received event either
// already admitted or deliberately held back for the next batch.
func (st *deliveryState) resetBatch() {
	st.pending, st.pendingBytes = nil, 0
}

// inFlightEvents is the number of Log_Events this goroutine holds that have been
// counted in flight and have reached no terminal state: the pending batch, plus
// the received-but-not-yet-appended event when there is one.
//
// It is exactly the number a recovered panic must discard (Req 8.3).
func (st *deliveryState) inFlightEvents() int {
	n := len(st.pending)
	if st.inbound != nil {
		n++
	}
	return n
}

// deliverOnce performs one iteration of the delivery select and reports whether
// the loop should stop and whether a panic was recovered.
//
// Three triggers put a batch on the wire, and each maps to one arm below:
//
//   - **Batch size** (Req 5.5). len(pending) reaching BatchSize transmits
//     immediately, in the same iteration that observed the count, so the 100 ms
//     bound is satisfied by there being no wait at all. The only thing that can
//     defer it is an in-progress transmission, which Req 5.15 says takes
//     precedence.
//   - **Flush tick** (Req 5.7, 5.12). A tick with a non-empty pending transmits
//     it; a tick with an empty pending does nothing whatsoever — zero HTTP
//     requests — and the ticker continues, which is the interval restarting.
//   - **The 1 MiB body cap** (Req 5.14). See fits.
//
// The rest are not delivery triggers: the health tick is the periodic summary of
// Req 11.6, flushReq is the synchronous Flush of Req 12.4, and closeDone is the
// drain of Req 9.3. They are received here because this goroutine owns pending;
// the policy behind them lives in logging/health.go, logging/flush.go, and
// logging/close.go respectively.
//
// An event that leaves the channel is counted in flight at once (Req 11.3), so
// the Req 11.4 identity holds at every instant: the event has left the derived
// queue depth and has not yet reached a terminal state.
//
// **The recovery (Req 8.3).** The deferred function below is the only recover in
// the delivery path, and it covers every arm rather than only transmit: the flush
// arm reaches transmit through drainAndTransmit, the close arm reaches the final
// drain, the health arm writes a diagnostic, and the queue arm can panic in the
// size accounting itself for an event whose length was never cached. It reports
// the panic instead of acting on the count, because the count belongs to run.
//
// The panic is contained but never hidden: recoverDeliveryPanic writes one ERROR
// Self_Diagnostic through s.diag, which is bound to a writer and unreachable from
// the fan-out handler, so reporting a delivery panic cannot itself produce a
// Log_Event or another delivery attempt (Req 8.1, 8.2).
func (s *Shipper) deliverOnce(ticker Ticker, health *healthMonitor, st *deliveryState) (stop, panicked bool) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		panicked = true
		// A panic on the close path ends the loop; a panic in any other arm
		// resumes it. Assigning the named results from here is what lets one
		// deferred function serve both.
		stop = st.draining
		s.recoverDeliveryPanic(st, p)
	}()

	queue := (<-chan *Event)(s.queue)

	select {
	case ev := <-queue:
		if ev == nil {
			// Unreachable: enqueue refuses a nil event, and nothing closes the
			// queue. Skipped rather than batched so a future caller bug cannot
			// put a JSON null on the wire, and skipped *before* the in-flight
			// increment so nothing is counted in flight that no terminal state
			// will ever release.
			return false, false
		}
		s.counters.addInFlight(1) // Req 11.3
		// Recorded before anything that can panic, so that a recovery discards
		// this event along with the batch instead of leaving it counted in flight
		// with nothing left to release it.
		st.inbound = ev
		if !s.fits(st.pending, st.pendingBytes, ev) {
			// Req 5.14: transmit what fits and retain ev for the next batch.
			s.transmit(st.pending)
			st.resetBatch()
		}
		st.admit(ev)
		if len(st.pending) >= st.batchSize {
			s.transmit(st.pending) // Req 5.5
			st.resetBatch()
		}
	case <-ticker.C():
		// A flush boundary is the only place a 402 suspension is lifted, so the
		// check goes before the transmit rather than after it: the batch pending
		// at the first boundary past the window is the first one to go on the
		// wire again (Req 7.10). It is a no-op in every other state, including
		// permanently disabled.
		s.resumeIfSuspensionElapsed()
		if len(st.pending) > 0 {
			s.transmit(st.pending) // Req 5.7
			st.resetBatch()
		}
		// An empty pending transmits nothing and the interval simply continues
		// (Req 5.12). There is deliberately no else branch: doing nothing is the
		// requirement.
	case <-health.tick():
		// Req 11.6, 11.7, 11.10: one INFO summary per 5-minute interval in which
		// a counter moved, nothing otherwise, nothing while disabled. It
		// transmits nothing, so it cannot delay a batch beyond the snapshot it
		// takes.
		s.maybeHealthSummary(health)
	case req := <-s.flushReq:
		st.pending = s.drainAndTransmit(st.pending, req)
		// Recomputed rather than tracked, because drainAndTransmit decides what
		// it leaves behind. It is O(len(pending)) and happens once per Flush, not
		// once per event, and the events it walks have cached lengths.
		st.pendingBytes = pendingEventBytes(st.pending)
	case <-s.closeDone:
		// Disabled Close returns immediately with a zero result and does not
		// wait for this goroutine. Exit without touching closeResult so the
		// caller's fast-path read cannot race a redundant zero publication.
		if s.state.Load() == stateDisabled {
			return true, false
		}
		// Set before the drain, so a panic inside it is recognised as a panic on
		// the close path and ends the loop.
		st.draining = true
		s.finalDrain(st.pending, st.inbound) // Req 9.3
		return true, false
	}
	return false, false
}

// recoverDeliveryPanic performs everything Req 8.3 requires of a recovered
// delivery panic apart from counting it: the in-flight Delivery_Batch is
// discarded and its events are counted as dropped, and exactly one ERROR
// Self_Diagnostic carrying the truncated panic value and stack is written.
//
// Both halves run inside containPanic, and that guard is the difference between
// degrading logging and killing the process. This function is called from a
// deferred function that is already recovering a panic, and a *second* panic
// raised there — from a diagnostic writer, in practice, since the counters cannot
// panic — would escape the delivery goroutine with nothing above it to recover
// and take the process down. A broken diagnostic writer must cost at most a
// missing diagnostic: a logging failure degrades logging and nothing else.
//
// The discard comes first for that same reason. If the writer is the thing that
// panics, the accounting has already been made whole before the diagnostic was
// attempted.
func (s *Shipper) recoverDeliveryPanic(st *deliveryState, p any) {
	containPanic(func() { s.discardPanickedBatch(st) })

	// diagPanic owns the truncation Req 8.3 specifies — the panic value to 1024
	// characters and the stack to 8192 bytes — so this call site cannot get
	// either bound wrong, and a delivery panic and the conversion panic of
	// Req 8.5 report in the same shape. The stack is captured from inside the
	// recovery, where the traceback still names the frame that panicked and not
	// merely the frame that recovered it.
	//
	// The number of discarded events is deliberately not repeated here. Req 8.3
	// asks for the panic value and the stack, the drop it caused is already in
	// the counters, and the periodic health summary reports it (Req 11.6).
	containPanic(func() { s.diagPanic(deliveryPanicMessage, p) })
}

// discardPanickedBatch discards the Delivery_Batch the delivery goroutine was
// holding when it panicked and returns how many Log_Events it discarded
// (Req 8.3).
//
// **Why the release is capped at the outstanding in-flight count.** transmit
// documents that each batch's in-flight release happens on exactly one of its
// terminal paths, and that abandonBatch performs its own — a second
// addInFlight(-n) double-counts and breaks the Req 11.4 identity. A panic can
// land on either side of that release: inside Send, which is where a delivery
// panic overwhelmingly comes from and where nothing has been released yet, or
// after it, in the diagnostic write that follows a terminal outcome, by which
// point transmit has already released *and* dropped this batch.
//
// The two cases are told apart by the counters themselves, and the invariant that
// makes that sound is Req 5.15 plus Req 5.3: one goroutine transmits, one
// Delivery_Batch at a time, and the in-flight count is adjusted by nobody else —
// the queue arm increments it, transmit and abandonBatch release it. So the events
// counted in flight at the instant of a panic are *this* goroutine's held batch,
// either whole or minus the part transmit has already accounted for. Releasing
// min(held, outstanding) therefore releases each event exactly once on both paths,
// and dropping the same number keeps the drop attribution in step with the
// release.
//
// The min is also the safe direction for a future caller that holds events in
// flight outside deliveryState — task 8.7's queue drain is the obvious candidate.
// Such a batch makes outstanding *larger* than held, which changes nothing here;
// were it ever smaller, this releases less rather than more, which leaves events
// counted in flight but can never double-count a terminal state.
//
// reasonAbandonedBatch is the reason, matching the design: it is what a
// Delivery_Batch that delivery could not finish is attributed to, alongside
// attempt exhaustion (Req 7.7) and the expiry of the close budget (Req 9.7). The
// abandoned-batch *counter* is deliberately not incremented — Req 7.7 defines it
// as batches that exhausted their transmission attempts, and a panicked batch
// exhausted nothing.
//
// The state is cleared before any counter is touched, so the same batch cannot be
// discarded twice however the rest of this function goes.
func (s *Shipper) discardPanickedBatch(st *deliveryState) int {
	held := st.inFlightEvents()
	st.inbound = nil
	st.resetBatch()

	if held <= 0 || s.counters == nil {
		return 0
	}
	if outstanding := s.Snapshot().InFlight; uint64(held) > outstanding {
		// The comparison only ever shrinks held, so the conversion is exact.
		held = int(outstanding)
	}
	if held <= 0 {
		return 0
	}

	// Release before recording the drop, which is the ordering the whole package
	// keeps: it returns the events to the derived queue depth for one instant,
	// whereas recording the drop first would count them as dropped and in flight
	// at the same time and break Req 11.4 for a Snapshot taken in between.
	s.releaseInFlight(held)
	s.counters.addDropped(reasonAbandonedBatch, uint64(held)) // Req 8.3
	return held
}

// disableAfterDeliveryPanics enters permanent disabled state after the 5th
// recovered panic and writes the one ERROR Self_Diagnostic Req 8.4 requires.
//
// The Event_Queue is deliberately *not* drained here, unlike the 403 path of
// disableAfterTokenRejection: Req 7.8 requires that drain and Req 8.4 does not.
// Events still resident in the channel stay in the derived queue depth, which is
// the honest place for an admitted event that was neither delivered nor
// discarded, and the Req 11.4 identity keeps holding for every later Snapshot.
// Every *new* admission from this point is dropped at enqueue and attributed to
// reasonNonRetryable (Req 8.9).
//
// The store is unconditional rather than a CompareAndSwap. Repeated delivery
// panics are a code defect, and Req 8.4 disables the Shipper for the remainder of
// the process lifetime, so this transition must win over a 402 suspension that
// happens to be in force — the opposite direction from suspendTransmission, which
// must never overwrite a disable. suspendUntil is cleared because a live deadline
// for a state nobody is in, which resumeIfSuspensionElapsed can never fire again,
// is misleading state.
//
// It runs on run's own frame rather than inside a recovery, so a panic from the
// diagnostic writer would escape the goroutine. containPanic is what keeps that
// from ending the process, exactly as in recoverDeliveryPanic.
func (s *Shipper) disableAfterDeliveryPanics(panics int) {
	s.state.Store(stateDisabled) // Req 8.4: for the remaining process lifetime
	s.suspendUntil = time.Time{}

	containPanic(func() {
		s.diagf(slog.LevelError, deliveryPanicDisabledMessage,
			slog.Int("panics", panics),
			slog.Int("panic_limit", maxDeliveryPanics),
			slog.String("endpoint", s.diagEndpoint()), // Req 10.5: no token, no query
		)
	})
}

// containPanic runs fn and absorbs any panic it raises.
//
// It is used only on the two paths that report a delivery panic, where an
// escaping panic has nothing above it to recover and would take the process down.
// Swallowing is the right disposition there and only there: the panic that
// matters has already been recovered and is about to be reported, and the one
// this absorbs can only come from the reporting itself.
//
// A panic recovered here is not re-reported. Describing a failing diagnostic
// writer through that same writer is the one thing guaranteed not to work, and an
// unbounded recursion of the attempt is how a contained failure becomes an
// outage.
func containPanic(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// transmit sends one Delivery_Batch and records its terminal state.
//
// Send is called **synchronously**, from the delivery goroutine, with no lock
// held. That single fact is what satisfies Req 5.15: this goroutine is the only
// one that transmits (Req 5.3), so while it is parked inside Send no other
// request can be started, and there is no batching or retry state for anything
// else to observe.
//
// Counter ordering is deliberate and is the ordering counters_test.go documents:
// the in-flight release happens **before** the delivered or dropped increment.
// Releasing first returns the events to the derived queue depth for one instant;
// recording the terminal outcome first would count them in two buckets at once
// and break the Req 11.4 identity for any snapshot taken in between.
//
// **Where each batch's in-flight release happens, exactly once.** There are four
// terminal paths out of this function and each releases the batch on exactly one
// of them:
//
//	suspended or disabled, no request  -> released in the cannotTransmit guard
//	retryable to the 5th attempt       -> released *inside* abandonBatch
//	close began during a backoff       -> released before the abandoned drop
//	any non-retryable terminal outcome -> released once the loop leaves
//
// abandonBatch owning its own release is the trap in this function: a second
// addInFlight(-n) on that path would double-count and break Req 11.4. The retry
// arm therefore returns immediately after calling it and never falls through to
// the terminal accounting below.
//
// ------------------------------------------------------------------------------
// SEAM. This function is the single place the delivery-outcome tasks edit, and
// they must MODIFY it in place — a second `func (s *Shipper) transmit` in another
// file of package logging is a redeclaration error, not an override.
//
// Wired here, from the files that own each rule:
//
//	classify (logging/classify.go)      what the attempt meant (Req 7.1-7.3, 7.10-7.15)
//	batchAttempt, retryTimer, abandonBatch (logging/retry.go)
//	                                    the 5-attempt ceiling, the backoff, Req 7.7
//	suspendTransmission, disableAfterTokenRejection (below)
//	                                    the 402 and 403 state transitions (Req 7.8-7.10)
//	diagOutcome (below)                 the one Self_Diagnostic each outcome calls for
//
// TASK 8.6 added nothing here, as promised: the recovery wraps the delivery
// loop, so a panic raised anywhere inside this function — most plausibly inside
// Send — is recovered by deliverOnce. The one thing it depends on from here is the
// release discipline documented above, which discardPanickedBatch reads back off
// the in-flight counter so that a panic on either side of a terminal release
// discards each event exactly once.
// ------------------------------------------------------------------------------
func (s *Shipper) transmit(batch []*Event) {
	if len(batch) == 0 {
		// Req 5.12: nothing to deliver, no request. The flush-tick arm already
		// checks this; the guard also covers the cap-triggered call, whose
		// pending is empty for the first event of a batch.
		return
	}
	n := len(batch)

	if s.cannotTransmit() {
		// Zero HTTP requests while disabled (Req 7.9) or suspended (Req 7.10),
		// and the batch reaches a terminal state here rather than lingering:
		// counting it keeps the Req 11.4 identity exact, whereas holding it
		// would either leave the events in flight for the whole 15-minute window
		// or grow pending past BatchSize and break the Req 6.6 bound.
		s.releaseInFlight(n)
		s.counters.addDropped(reasonNonRetryable, uint64(n))
		return
	}

	// One batchAttempt per Delivery_Batch (Req 7.6). It is a local, not a
	// Shipper field, so the count cannot leak into the next batch: this function
	// handles exactly one, and the zero value means "not yet transmitted".
	var st batchAttempt

	for {
		// The context lives for the Shipper and is cancelled when Close begins,
		// so an ordinary request already in progress cannot consume more than the
		// fixed close budget before the final drain gets control. finalDrain uses
		// its own budget context for its one-attempt requests.
		outcome := s.client.Send(s.deliveryRequestContext(), batch, 0)
		st.record(outcome)
		c := classify(outcome)

		if c.retriesBatch() {
			if st.verdict(true) == retryVerdictAbandon {
				// Req 7.7. abandonBatch performs the entire terminal
				// accounting, the in-flight release included, and writes the one
				// ERROR the requirement calls for. Returning here rather than
				// breaking out is what keeps that release single.
				s.abandonBatch(batch, st)
				return
			}
			if !s.waitBeforeRetransmit(st.attempts, outcome) {
				// The close operation began, and Req 9.12 allows it zero backoff
				// waits, so this batch is not retransmitted. It is discarded
				// here rather than left counted in flight for the rest of the
				// process: reasonAbandonedBatch is the reason the design gives a
				// Delivery_Batch that delivery ran out of time for (Req 9.7).
				s.releaseInFlight(n)
				s.counters.addDropped(reasonAbandonedBatch, uint64(n))
				return
			}
			// After the wait, because Req 7.2 and 7.3 count a retransmission and
			// not a retryable attempt: a wait that the close operation cut short
			// produced no retransmission to count.
			s.counters.addRetriedBatch()
			continue
		}

		// Release before recording the terminal outcome. See the ordering note
		// above.
		s.releaseInFlight(n)
		if c.creditsDelivered() {
			s.counters.addDelivered(uint64(n)) // Req 7.1, 7.14
		} else {
			// Req 7.10, 7.11, 7.15, all attributed to reasonNonRetryable by
			// classification.dropReason. outcomeNoRequest also lands here: Send
			// cannot return it for a non-empty batch, and counting a drop is the
			// disposition that keeps the identity exact if it ever does.
			s.counters.addDropped(c.dropReason(), uint64(n))
		}

		// The two state transitions, and the only place either is entered.
		discarded := n
		switch c.outcome {
		case outcomeSuspend:
			s.suspendTransmission() // Req 7.10
		case outcomeDisable:
			// The drained events are discarded too, and their number is only
			// knowable here — which is why classify cannot supply it (Req 7.8).
			discarded += s.disableAfterTokenRejection()
		}

		s.diagOutcome(c, outcome.Status, discarded)
		return
	}
}

// waitBeforeRetransmit waits the retry delay for the next retransmission and
// reports whether that retransmission may proceed (Req 7.4, 7.5).
//
// The delay comes from retryTimer, so it is measured on the injected clock and
// recorded there for a test to read back (Req 12.2), and the Retry-After value
// goes through retryAfterForStatus rather than being taken from the outcome
// directly: Req 7.4 honours the header for HTTP 429 alone, so a 5xx that happens
// to carry one still follows the doubling schedule.
//
// It reports false the moment the close operation begins, which is the design's
// Backoff -> Draining edge and what makes Req 9.12 — a single transmission
// attempt per Delivery_Batch and zero backoff waits once close is in progress —
// hold for a batch that was already mid-backoff. Without it, a Close arriving
// during a 30 s backoff would have to wait the backoff out, and a delivery
// goroutine waiting on a clock nothing will advance would never exit at all.
//
// The non-blocking check comes first so that an already-begun close costs no
// wait, and so that no delay is drawn from the jitter source for a wait nobody
// will perform.
//
// A nil closeDone belongs to a Shipper composed literally rather than through
// NewShipper. Receiving from it can never proceed, which is the right answer:
// such a Shipper has no close operation to be in progress.
func (s *Shipper) waitBeforeRetransmit(attempt int, outcome SendOutcome) bool {
	select {
	case <-s.closeDone:
		return false // Req 9.12: zero backoff waits during close
	default:
	}

	timer, _ := s.retryTimer(attempt, retryAfterForStatus(outcome.Status, outcome.RetryAfter))
	select {
	case <-timer:
		return true
	case <-s.closeDone:
		return false
	}
}

// releaseInFlight returns n events of a Delivery_Batch that reached a terminal
// state to the derived queue depth (Req 11.3).
//
// It exists so that "the in-flight release happens exactly once per batch" is
// auditable by grepping one name rather than by reading every counter call in
// this file. abandonBatch performs its own release and deliberately does not go
// through this helper, because its release belongs to the abandonment path it
// owns (Req 7.7).
func (s *Shipper) releaseInFlight(n int) {
	s.counters.addInFlight(-int64(n))
}

// cannotTransmit reports whether this Shipper must issue zero HTTP requests
// right now.
//
// stateDisabled is Req 7.9 and Req 1.11: zero requests for the remaining
// lifetime of the process, including at flush boundaries and during Close.
// stateSuspended is the 15-minute window of Req 7.10, during which enqueue stays
// open (Req 7.12) but nothing is transmitted.
//
// A nil client is unreachable through NewShipper, which always constructs one;
// it is checked so that a Shipper composed literally in a test drops its batch
// instead of taking the process down with a nil dereference on the delivery
// goroutine.
func (s *Shipper) cannotTransmit() bool {
	return s.client == nil || s.state.Load() != stateEnabled
}

// suspendTransmission enters the 15-minute suspension HTTP 402 calls for
// (Req 7.10).
//
// Only the state transition and the deadline live here. The batch that drew the
// 402 has already been dropped by transmit, and the ERROR is written by
// diagOutcome once this returns, so this function has exactly one job and the
// "one diagnostic per outcome" rule stays visible at the call site.
//
// The transition is a CompareAndSwap from enabled rather than a Store, because a
// suspension must never overwrite a permanent disable: stateDisabled is for the
// remaining lifetime of the process (Req 7.9), and a 402 arriving from a batch
// that was already in flight when a 403 landed would otherwise resurrect
// transmission 15 minutes later.
//
// The deadline is read from the injected clock, so a test drives the whole window
// by advancing a fake one (Req 12.2), and it is set only after the swap succeeds
// so that suspendUntil is never a live deadline for a state nobody entered.
func (s *Shipper) suspendTransmission() {
	if !s.state.CompareAndSwap(stateEnabled, stateSuspended) {
		return
	}
	s.suspendUntil = s.deliveryClock().Now().Add(suspensionWindow)
}

// resumeIfSuspensionElapsed lifts a 402 suspension whose 15 minutes have passed,
// and is called only from run's flush-tick arm — which is what makes the
// resumption happen "at the first flush interval boundary after the 15 minutes
// elapse" (Req 7.10) rather than at the next arriving event.
//
// It is a no-op in every other state. In particular a permanently disabled
// Shipper is never re-enabled by it: the state is not stateSuspended, so the
// first check returns, and the CompareAndSwap could not fire even if it did not.
//
// !Before rather than After, so a boundary landing exactly on the deadline
// resumes. The suspension is a floor on how long transmission stops, and the
// requirement's "after the 15 minutes elapse" is satisfied at the instant they
// have.
func (s *Shipper) resumeIfSuspensionElapsed() bool {
	if s.state.Load() != stateSuspended {
		return false
	}
	if s.deliveryClock().Now().Before(s.suspendUntil) {
		return false
	}
	s.suspendUntil = time.Time{}
	return s.state.CompareAndSwap(stateSuspended, stateEnabled)
}

// disableAfterTokenRejection enters permanent disabled state after HTTP 403 and
// discards every Log_Event still held in the Event_Queue, returning how many it
// discarded (Req 7.8, 7.9).
//
// The order is deliberate: the state is stored **before** the drain, so a
// producer that reaches enqueue while the drain is running takes the disabled
// branch and is counted there (Req 8.9) instead of refilling a queue that has
// already been emptied. A producer that had passed the state check before the
// store can still land one event in the channel afterwards; that event is not
// lost from the accounting — it stays in the derived queue depth, which is
// exactly where an admitted, undelivered, undiscarded event belongs — and no
// later transmission can ever pick it up, because every transmit call from now on
// stops at cannotTransmit.
//
// The drain is non-blocking: the default arm of the select ends it the moment the
// channel is empty, so it cannot wait for a producer, and it terminates after at
// most QueueSize receives.
//
// The drops are counted in one addDropped call after the loop rather than one per
// event. Both keep the Req 11.4 identity exact — the drained events sit in the
// derived queue depth until they are counted, and the derived depth is computed
// from the counters rather than from len(chan) — and a single call takes the
// counters mutex once instead of QueueSize times.
//
// The events are *not* counted in flight first. They never left the queue into a
// Delivery_Batch, so they were never in flight; they move straight from the
// derived queue depth to dropped.
func (s *Shipper) disableAfterTokenRejection() int {
	// Serialize the permanent state transition and channel drain with enqueue's
	// state-check/send critical section. Without this lock a producer could pass
	// the enabled check, pause, and send after the drain observed an empty queue,
	// permanently stranding an admitted event (Req 7.8, 8.9).
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.state.Store(stateDisabled) // Req 7.8, 7.9: for the remaining lifetime
	s.suspendUntil = time.Time{}

	drained := 0
	for {
		select {
		case <-s.queue:
			drained++
		default:
			if drained > 0 {
				s.counters.addDropped(reasonNonRetryable, uint64(drained))
			}
			return drained
		}
	}
}

// diagOutcome writes the single Self_Diagnostic the classification calls for, or
// nothing at all when it calls for none.
//
// classify decides *that* a diagnostic is required and what it must contain;
// this is where the text is produced. Keeping the two apart is what lets the
// status table be tested as a pure function while the wording — which no
// requirement fixes verbatim — stays in the one place that can also reach
// s.diagEndpoint.
//
// What each requirement asks for, and where it is satisfied:
//
//	diagWarnStatus       WARN naming the status                  Req 7.14
//	diagErrorStatusCount ERROR with the status and the count     Req 7.11
//	diagErrorStatus      ERROR with the status                   Req 7.15
//	diagErrorQuota       ERROR about account or quota rejection  Req 7.10
//	diagErrorToken       ERROR that the Source_Token was rejected Req 7.8
//
// The Source_Token appears in none of them. Nothing here reads cfg.SourceToken,
// the endpoint is rendered by diagEndpoint, which strips userinfo and the query
// string, and the token diagnostic reports only *that* the token was rejected
// (Req 7.13, 10.4, 10.5).
//
// discarded is the number of Log_Events this outcome discarded, which for
// diagErrorToken includes the ones drained out of the Event_Queue and not merely
// the transmitted batch (Req 7.8).
func (s *Shipper) diagOutcome(c classification, status, discarded int) {
	if c.diag == diagNone {
		return
	}

	args := make([]any, 0, 4)
	// A status is reported whenever one arrived, which is a superset of the
	// three requirements that mandate it: an operator reading the 402 or 403
	// ERROR is better off seeing 402 than inferring it from the wording.
	if c.diag.namesStatus() || status > 0 {
		args = append(args, slog.Int("status", status))
	}
	if c.diag.namesCount() || c.creditsDropped() {
		args = append(args, slog.Int("discarded_events", discarded))
	}
	args = append(args, slog.String("endpoint", s.diagEndpoint())) // Req 10.5
	if c.diag == diagErrorQuota {
		args = append(args, slog.Float64("suspended_seconds", suspensionWindow.Seconds()))
	}

	s.diagf(c.diag.level(), diagOutcomeMessage(c.diag), args...)
}

// diagOutcomeMessage is the operator-facing text of each required diagnostic.
//
// Each string says what its requirement says: 7.14 that the endpoint answered
// with an unexpected success status, 7.11 and 7.15 that a batch was discarded,
// 7.10 that Better Stack rejected the batch for account or quota reasons, and
// 7.8 that the source token was rejected. None of them contains a token, and
// none of them is derived from a response body — only from the status.
func diagOutcomeMessage(d diagRequirement) string {
	switch d {
	case diagWarnStatus:
		return "betterstack ingest accepted the delivery batch with an unexpected success status"
	case diagErrorStatusCount:
		return "betterstack ingest rejected the delivery batch: log events discarded"
	case diagErrorQuota:
		return "betterstack rejected the delivery batch for account or quota reasons: transmission suspended"
	case diagErrorToken:
		return "betterstack rejected the configured source token: log delivery permanently disabled"
	default:
		return "betterstack ingest returned a non-retryable status: delivery batch discarded"
	}
}

func (s *Shipper) deliveryRequestContext() context.Context {
	if s != nil && s.closeCtx != nil {
		return s.closeCtx
	}
	return context.Background()
}

// deliveryClock returns the Clock the delivery goroutine drives its ticker from.
//
// NewShipper always sets s.clock through applyDefaults, so the fallback exists
// only for a Shipper composed literally: NewTicker on a nil Clock would panic on
// the delivery goroutine, which is a poor way to learn about a missing field.
func (s *Shipper) deliveryClock() Clock {
	if s.clock != nil {
		return s.clock
	}
	return SystemClock()
}

// flushInterval returns the flush interval in force, never a non-positive
// duration.
//
// ResolveConfig clamps FlushEvery to 1..300 s, so a non-positive value can only
// come from a hand-assembled Config — and both SystemClock.NewTicker and the
// test fake panic on one, which would take the process down from the delivery
// goroutine. The documented default is applied instead.
func (s *Shipper) flushInterval() time.Duration {
	if s.cfg.FlushEvery > 0 {
		return s.cfg.FlushEvery
	}
	return defaultFlushSeconds * time.Second
}

// batchSize returns the maximum Delivery_Batch size in force, never
// non-positive.
//
// ResolveConfig clamps BatchSize to 1..1000. A non-positive value from a
// hand-assembled Config would make the batch-size trigger fire on every single
// event, quietly turning one request per batch into one request per event, so
// the documented default is applied instead (Req 5.4).
func (s *Shipper) batchSize() int {
	if s.cfg.BatchSize > 0 {
		return s.cfg.BatchSize
	}
	return defaultBatchSize
}

// fits reports whether ev can join a pending batch that already holds
// len(pending) events totalling pendingBytes serialized bytes, without pushing
// the request body over maxBatchBodyBytes (Req 5.14).
//
// It is O(1). The batch is never re-marshalled: each event's serialized length
// was computed once by capEventSize at conversion time and cached on the Event,
// and run keeps the running total. A per-event re-marshal of the whole pending
// batch would be O(BatchSize) work per arrival — up to 1000 marshals of up to
// 1 MiB — on the one goroutine that also has to keep the queue drained.
//
// An empty pending always fits, which is what makes "a batch always holds at
// least one event" true (property P15). Returning false there would strand an
// oversized event: transmit would send an empty batch, pending would stay empty,
// and the same event would be refused forever. It is defensive rather than load
// bearing, because capEventSize caps every event at 65536 bytes and the body cap
// is 1048576.
func (s *Shipper) fits(pending []*Event, pendingBytes int, ev *Event) bool {
	if len(pending) == 0 {
		return true
	}
	return batchBodyBytes(len(pending)+1, pendingBytes+eventBytes(ev)) <= maxBatchBodyBytes
}

// batchBodyBytes returns the exact size of the JSON body IngestClient.Send will
// produce for a batch of count events whose serialized lengths total
// eventTotal: one '[', one ']', and count-1 commas (Req 5.8).
//
// Computing the separators rather than approximating them is what lets the cap
// be asserted as an exact byte bound in property P15 instead of as an estimate
// with slack.
func batchBodyBytes(count, eventTotal int) int {
	if count <= 0 {
		return emptyBatchBodyBytes
	}
	return eventTotal + count + 1
}

// eventBytes returns the number of body bytes ev contributes to a
// Delivery_Batch.
//
// SerializedLen documents 0 as a cache miss, not as an empty event — no JSON
// encoding of an Event is shorter than its six required top-level fields — so 0
// is never trusted. The event is measured instead, and the measurement is cached
// back onto it: by the time run sees an event it has left the Event_Queue and is
// owned solely by the delivery goroutine, so the write races with nothing.
//
// A miss is unreachable from the Handle path, where capEventSize always records a
// length; it happens for a hand-built Event in a test.
//
// An event that will not marshal at all is charged the full per-event cap rather
// than 0. Charging 0 would let an unbounded number of unmeasurable events into
// one body and turn a cap violation into a silent 413; charging the cap can only
// split a batch one event early.
func eventBytes(ev *Event) int {
	if ev == nil {
		return 0
	}
	if n := ev.SerializedLen(); n > 0 {
		return n
	}
	if n, ok := serializedSize(ev); ok {
		ev.SetSerializedLen(n)
		return n
	}
	return maxEventBytes
}

// pendingEventBytes totals the serialized lengths of a batch. run uses it to
// re-establish its running total after an arm that rewrote pending wholesale.
func pendingEventBytes(pending []*Event) int {
	total := 0
	for _, ev := range pending {
		total += eventBytes(ev)
	}
	return total
}

// Handler returns the Better_Stack_Handler for this Shipper, thresholded at the
// configured Shipping_Threshold (Req 2.1).
//
// For a Shipper disabled at startup it returns a conversion-only handler — one
// whose shipper is nil — rather than a handler bound to this Shipper. That is not
// a shortcut; it is what Req 11.11 requires. main.go installs
// NewFanoutHandler(console, shipper.Handler()) unconditionally, and Handle counts
// an admission before conversion, so a handler bound to a disabled Shipper would
// increment enqueued and dropped on every ERROR record and no snapshot would
// report the zeros Req 11.11 demands. With a nil shipper, Handle takes its first
// cheap exit: zero counters, zero conversion work, zero enqueues, and the console
// child of the fan-out still writes every record (Req 1.10, 1.11, 12.3).
//
// A nil *Handler is deliberately never returned. NewFanoutHandler drops nil
// children by comparing the slog.Handler interface against nil, and a typed nil
// *Handler stored in that interface is not nil — it would survive the filter and
// panic on the first Enabled call.
//
// Enabled reports the threshold answer either way, so the level gate behaves
// identically for an enabled and a disabled Shipper (Req 2.9, 2.10).
//
// Mid-life disabling is different and is handled on the enqueue path instead: a
// Shipper that was enabled at startup hands out a bound handler, and if it later
// disables — HTTP 403 (Req 7.8) or five recovered delivery panics (Req 8.4) —
// enqueue discards without enqueueing (Req 8.9).
func (s *Shipper) Handler() *Handler {
	if s == nil {
		return NewHandler(nil, slog.LevelError)
	}
	if s.state.Load() == stateDisabled {
		return NewHandler(nil, s.cfg.MinLevel)
	}
	return NewHandler(s, s.cfg.MinLevel)
}

// Snapshot returns one consistent observation of the Shipper's counters
// (Req 11.3).
//
// It never panics, including on a disabled Shipper, and mutates nothing
// (Req 11.10). For a Shipper disabled at startup every field is zero, because
// nothing was ever counted: Handler hands out a conversion-only handler, so no
// admission is recorded, and Start runs no goroutine, so nothing is delivered or
// dropped (Req 11.11).
//
// The nil guards cover a Shipper composed literally rather than through
// NewShipper. Req 11.10 says the snapshot method must not panic, and a health
// endpoint that crashes the process while reporting that logging is degraded is
// the worst possible failure mode for this method.
func (s *Shipper) Snapshot() Snapshot {
	if s == nil || s.counters == nil {
		return Snapshot{}
	}
	return s.counters.snapshot()
}

// enqueue offers ev to the Event_Queue without waiting for queue capacity or
// for network progress.
//
// The channel send remains non-blocking. lifecycleMu is held only across the
// closed/disabled checks and that send, making the final empty-queue observation
// in Close atomic with admission while keeping the operation inside the 1 ms
// bounds of Req 5.2, 5.13, 6.3, 9.4, and 9.11.
//
// Every path increments exactly one counter or enqueues exactly one event, so
// each admission Handle counted lands in exactly one bucket of the Req 11.4
// accounting identity.
//
// The two refusal cases and why each takes the reason it does:
//
//   - **closed** means the close operation has returned. Req 9.11 names this drop
//     explicitly, and it is what reasonAfterClose exists for. The console keeps
//     writing the record, because the fan-out's console child is not on this path
//     at all.
//
//   - **disabled** is a Shipper that was enabled at startup and has since stopped
//     for good (Req 8.9). It is attributed to reasonNonRetryable rather than to a
//     sixth reasonDisabled reason, which is a deliberate choice: Req 11.5
//     enumerates exactly five drop reasons and requires their sum to equal the
//     total, so a sixth would change numDropReasons and contradict the
//     requirement and the counter tests that assert against it. reasonNonRetryable
//     is the honest attribution of the two ways this state is reached — HTTP 403,
//     a non-retryable ingest response (Req 7.8), and five recovered delivery
//     panics (Req 8.4) — and reasonAfterClose is left to mean only what Req 9.11
//     defines it to mean.
//
// A suspended Shipper is not refused: Req 7.12 requires enqueue to stay open for
// the whole 15-minute window, which the non-blocking send below does.
func (s *Shipper) enqueue(ev *Event) {
	if ev == nil {
		// Unreachable from Handle, whose convertRecord always returns an event.
		// Counted rather than ignored so that a future caller bug shows up as a
		// drop instead of as an admission with no terminal state, which would
		// silently break the Req 11.4 identity.
		s.counters.addDropped(reasonConversionPanic, 1)
		return
	}

	queueFull := false
	s.lifecycleMu.Lock()
	switch {
	case s.closed.Load():
		s.counters.addDropped(reasonAfterClose, 1) // Req 9.11
	case s.state.Load() == stateDisabled:
		s.counters.addDropped(reasonNonRetryable, 1) // Req 8.9
	default:
		select {
		case s.queue <- ev:
		default:
			s.counters.addDropped(reasonQueueFull, 1) // Req 6.3
			queueFull = true
		}
	}
	s.lifecycleMu.Unlock()

	if queueFull {
		// The diagnostic write stays outside lifecycleMu: a slow diagnostic
		// writer cannot delay either another enqueue or the final close seal.
		s.maybeDiagDrop()
	}
}
