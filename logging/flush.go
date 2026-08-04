package logging

// The synchronous flush of Req 12.4: the operation that lets a test assert on
// delivered payloads without polling, and lets a caller force everything the
// Shipper is holding onto the wire without shutting it down.
//
// Two halves live here, and the split matters:
//
//   - Flush, below, runs on the *caller's* goroutine. It owns the deadline — the
//     clamp to 1 ms..60 s, the wait, and the answer returned when the wait runs
//     out — and it touches no batching state whatsoever.
//   - drainAndTransmit runs on the *delivery* goroutine, reached from the flushReq
//     arm of deliverOnce. It owns the drain and the transmissions, because it is
//     the only goroutine allowed to hold a pending Delivery_Batch or to call
//     transmit (Req 5.3, 5.15).
//
// drainAndTransmit was moved here from logging/shipper.go rather than
// redeclared. finalDrain follows the same ownership rule and now lives in
// logging/close.go.
//
// The one thing Flush must never do is make the delivery goroutine wait for the
// caller. The reply channel is buffered and every send onto it is non-blocking,
// so a caller that gave up at its deadline and walked away cannot wedge the
// goroutine that every other Log_Event depends on.

import "time"

// Bounds on the caller-supplied flush deadline (Req 12.4).
//
// They are clamped rather than rejected, because Flush has no error return and a
// nonsense deadline is a caller bug that must not turn into an unbounded wait or
// into a flush that expires before the request is even delivered. 0 and negative
// durations become 1 ms, which still lets the delivery goroutine be reached; a
// caller asking for an hour gets a minute.
const (
	minFlushDeadline = time.Millisecond
	maxFlushDeadline = 60 * time.Second
)

// Flush transmits everything the Shipper is holding and waits for it to reach a
// terminal state, bounded by deadline (Req 12.4).
//
// It reports how many Log_Events this flush delivered, how many it dropped, and
// whether the Event_Queue drained fully. The Shipper stays usable afterwards:
// nothing here closes a channel, stops the delivery goroutine, or changes the
// state — which is the whole difference between this and Close.
//
// **The deadline is measured on the injected Clock**, like every other wait in the
// package (Req 12.2), so a test with a fake clock that expects the deadline to
// expire advances that clock rather than sleeping. In production the Clock is
// SystemClock and the deadline is wall-clock time.
//
// A flush that no delivery goroutine can serve returns at once rather than after
// the deadline, in the three cases below. Each is a state in which a flush can
// achieve nothing, so waiting for it would only slow the caller down:
//
//   - **Closed.** The delivery goroutine has already returned, so nothing would
//     ever receive the request (Req 9.9).
//   - **Disabled.** Zero HTTP requests for the remaining lifetime of the process,
//     including at flush boundaries (Req 1.11, 7.9, 8.4). A Shipper disabled at
//     startup has no delivery goroutine at all (Req 12.3).
//   - **Closing.** closeDone is closed, so the goroutine is in or past its final
//     drain, an arm it never leaves.
//
// The one case that is deliberately *not* short-circuited is a 402 suspension. It
// is transient and the delivery goroutine is still running; drainAndTransmit
// applies the delivery loop's existing suspended-state terminal accounting while
// issuing zero HTTP requests.
//
// The counts on every early-return path are deltas over this call: a Snapshot is
// taken before the request is sent and differenced against one taken when the
// wait ends, so a flush that timed out still reports what the delivery goroutine
// managed to deliver while it waited.
func (s *Shipper) Flush(deadline time.Duration) FlushResult {
	if s == nil {
		// A nil Shipper holds nothing, so a flush of it has drained fully. Nil
		// guards elsewhere in this package exist for the same reason Snapshot's
		// do: an observability call must not be the thing that panics.
		return FlushResult{Drained: true}
	}

	before := s.Snapshot()
	if s.flushUnservable() {
		return s.flushOutcome(before)
	}

	d := clampFlushDeadline(deadline)
	// One expiry channel for the whole call, drawn before the request is sent, so
	// that the time spent handing the request over counts against the caller's
	// deadline exactly as the time spent waiting for the reply does.
	expiry := s.deliveryClock().After(d)

	// Buffered, so drainAndTransmit's reply lands even if this goroutine has
	// already stopped listening.
	req := flushRequest{deadline: d, reply: make(chan FlushResult, 1)}

	select {
	case s.flushReq <- req:
	case <-expiry:
		return s.flushSettled(req.reply, before)
	case <-s.closeDone:
		return s.flushSettled(req.reply, before)
	}

	select {
	case result := <-req.reply:
		return result
	case <-expiry:
		return s.flushSettled(req.reply, before)
	case <-s.closeDone:
		return s.flushSettled(req.reply, before)
	}
}

// flushUnservable reports whether no delivery goroutine will ever receive a flush
// request from this Shipper. See Flush for the three cases and why each one is
// answered immediately.
//
// A nil closeDone belongs to a Shipper composed literally rather than through
// NewShipper. Receiving from a nil channel can never proceed, so the default arm
// is taken and such a Shipper is treated as not closing — which is correct: it has
// no close operation.
func (s *Shipper) flushUnservable() bool {
	if s.closed.Load() {
		return true // Req 9.9: the delivery goroutine has returned
	}
	if s.state.Load() == stateDisabled {
		return true // Req 1.11, 7.9, 8.4: zero requests, and possibly no goroutine
	}
	select {
	case <-s.closeDone:
		return true
	default:
		return false
	}
}

// flushSettled answers a flush whose wait ended without a reply being received in
// the select — either the deadline expired or the close operation began.
//
// It checks the reply buffer once more before falling back to the counters,
// because the two events can happen in the same instant: the delivery goroutine
// may have finished the drain and buffered its report while this goroutine was
// being woken by the expiry. Its report is the better answer, since it knows what
// the drain did, so it wins whenever it exists.
func (s *Shipper) flushSettled(reply chan FlushResult, before Snapshot) FlushResult {
	select {
	case result := <-reply:
		return result
	default:
		return s.flushOutcome(before)
	}
}

// flushOutcome reports what the counters say happened since before.
//
// Drained is answered observationally rather than by assuming a timed-out flush
// left work behind: a queue with nothing resident and no Log_Event in flight *has*
// drained, whether or not this call is what drained it, and a caller asking "is
// there anything left" is better served by the true answer than by a pessimistic
// one. The queue length and the Snapshot are read a few nanoseconds apart, so a
// concurrent producer can make the pair momentarily disagree — which costs a
// false negative on a queue that is being written to, never a false positive on
// one that is not.
func (s *Shipper) flushOutcome(before Snapshot) FlushResult {
	after := s.Snapshot()
	return FlushResult{
		Delivered: subFloor(after.Delivered, before.Delivered),
		Dropped:   subFloor(after.Dropped, before.Dropped),
		Drained:   s.queueSettled(after),
	}
}

// queueSettled reports whether the Shipper is holding no admitted Log_Event: none
// resident in the Event_Queue and none in flight in a Delivery_Batch (Req 11.3).
//
// That conjunction is what "the Event_Queue drained fully" (Req 12.4) has to mean
// for a caller: an event the delivery goroutine has taken out of the channel but
// not yet finished transmitting is still pending work, and reporting a drained
// queue while one is in flight would be the misleading half of the answer.
func (s *Shipper) queueSettled(snap Snapshot) bool {
	return len(s.queue) == 0 && snap.InFlight == 0
}

// clampFlushDeadline brings a caller-supplied deadline into the 1 ms..60 s range
// Req 12.4 specifies.
func clampFlushDeadline(d time.Duration) time.Duration {
	switch {
	case d < minFlushDeadline:
		return minFlushDeadline
	case d > maxFlushDeadline:
		return maxFlushDeadline
	}
	return d
}

// drainAndTransmit is the flushReq arm of the delivery loop: it empties the
// Event_Queue into Delivery_Batches, transmits them, reports the result to the
// caller, and returns the batch that remains pending afterwards.
//
// **Moved here from logging/shipper.go, not redeclared.** See the file comment.
//
// It runs on the delivery goroutine, so it may do everything that goroutine may
// do and nothing else may: receive from the queue, hold a pending batch, and call
// transmit synchronously. transmit's own guarantees carry over unchanged — one
// batch on the wire at a time (Req 5.15), the retry ladder and its 5-attempt
// ceiling (Req 7.6), and the terminal accounting of every event it takes.
//
// **What bounds the drain.** The loop is bounded by the number of Log_Events
// resident in the queue at the instant the request arrived, which is exactly
// Req 12.4's "every queued Log_Event": the events the caller had already emitted
// when it asked. A loop that instead drained until the channel reported empty
// would be unbounded in the presence of concurrent producers — a Shipper under
// sustained load would keep this goroutine in the drain indefinitely, which is
// both a live-lock for the flush tick and, on a fake clock, a loop no deadline
// can interrupt. Events enqueued *during* the drain are left for the ordinary
// triggers, and they are why Drained can come back false from a drain that did
// empty everything it set out to.
//
// **The deadline.** It is a second bound, not the primary one, and it matters
// when the transmissions are slow rather than when the queue is deep: 1000 events
// at up to 5 attempts each can outlast any deadline a caller would choose. It is
// checked between batches rather than mid-transmission, because transmit is
// synchronous and interrupting a Delivery_Batch that is already on the wire would
// mean either abandoning events that may yet be accepted or double-counting them.
//
// **Suspension and permanent disable need no special case here.** transmit issues
// zero requests in either state and accounts for the batch it was given, so a
// flush that arrives during a 402 window reports drops rather than deliveries and
// the caller sees Drained honestly. Flush short-circuits the disabled case before
// a request is ever sent, so in practice this function only sees it if a 403
// landed while the request was in transit.
//
// The reply is sent non-blockingly onto a buffered channel: a caller whose
// deadline expired is gone, and the delivery goroutine must never be parked on an
// abandoned channel.
func (s *Shipper) drainAndTransmit(pending []*Event, req flushRequest) []*Event {
	before := s.Snapshot()

	// Re-clamped rather than trusted: Flush always sends a clamped deadline, but a
	// zero-valued flushRequest reaching here would otherwise ask the clock for
	// After(0), which fires immediately and would turn every flush into a no-op.
	expiry := s.deliveryClock().After(clampFlushDeadline(req.deadline))

	batchSize := s.batchSize()
	pendingBytes := pendingEventBytes(pending)
	resident := len(s.queue)
	timedOut := false

	for i := 0; i < resident; i++ {
		if flushExpired(expiry) {
			timedOut = true
			break
		}

		ev, ok := s.dequeue()
		if !ok {
			// Unreachable with a single consumer: resident events cannot leave the
			// channel any other way. Breaking rather than continuing keeps the loop
			// from spinning if that ever stops being true.
			break
		}
		s.counters.addInFlight(1) // Req 11.3: out of the queue, no terminal state yet

		if !s.fits(pending, pendingBytes, ev) {
			// Req 5.14, the same 1 MiB body cap the queue arm applies: transmit what
			// fits and start the next batch with ev.
			s.transmit(pending)
			pending, pendingBytes = nil, 0
		}
		pending = append(pending, ev)
		pendingBytes += eventBytes(ev)

		if len(pending) >= batchSize {
			s.transmit(pending) // Req 5.5: never more than BatchSize per request
			pending, pendingBytes = nil, 0
		}
	}

	// The partial batch is the point of a flush: it is what the ordinary triggers
	// would have held until the next interval boundary. It is transmitted unless
	// the deadline has gone, in which case it is handed back to the delivery loop
	// still pending — the events stay counted in flight, which is what they are,
	// and the next trigger sends them.
	if !timedOut && flushExpired(expiry) {
		timedOut = true
	}
	if !timedOut && len(pending) > 0 {
		s.transmit(pending)
		pending = nil
	}

	after := s.Snapshot()
	replyFlush(req, FlushResult{
		Delivered: subFloor(after.Delivered, before.Delivered),
		Dropped:   subFloor(after.Dropped, before.Dropped),
		Drained:   !timedOut && len(pending) == 0 && s.queueSettled(after),
	})
	return pending
}

// dequeue takes one Log_Event out of the Event_Queue without blocking, reporting
// whether it got one.
//
// Non-blocking is not an optimisation here, it is the whole contract: a blocking
// receive in the drain would park the delivery goroutine on an empty queue with no
// way back to the select, so a flush of a queue shallower than it looked would
// stop delivery entirely until the next event arrived.
//
// A nil event is refused for the reason the queue arm gives: nothing enqueues one,
// and treating one as absent keeps a JSON null off the wire.
func (s *Shipper) dequeue() (*Event, bool) {
	select {
	case ev := <-s.queue:
		if ev == nil {
			return nil, false
		}
		return ev, true
	default:
		return nil, false
	}
}

// flushExpired reports whether the flush deadline has passed, without waiting for
// it.
//
// The channel comes from Clock.After and carries at most one value, so this both
// tests and consumes the expiry — every later call answers false. That is why the
// drain records the answer in a local the moment it is true rather than asking
// again.
func flushExpired(expiry <-chan time.Time) bool {
	select {
	case <-expiry:
		return true
	default:
		return false
	}
}

// replyFlush hands a result back to the caller of Flush, and never blocks.
//
// A nil reply channel is a flushRequest that came from somewhere other than
// Flush; there is nobody to answer, and the drain has already done its work.
func replyFlush(req flushRequest, result FlushResult) {
	if req.reply == nil {
		return
	}
	select {
	case req.reply <- result:
	default:
	}
}
