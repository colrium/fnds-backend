package logging

import (
	"math"
	"sync"
)

// DropReason classifies why a Log_Event that was admitted for shipping never
// reached the ingest endpoint. Every drop is attributed to exactly one reason so
// that the sum of the per-reason counters equals the total dropped counter in
// every Snapshot (Req 11.5).
type DropReason int

const (
	// reasonQueueFull is a drop caused by the Event_Queue being at capacity
	// when an enqueue was attempted (Req 6.3).
	reasonQueueFull DropReason = iota
	// reasonAbandonedBatch is a drop caused by a Delivery_Batch exhausting its
	// 5 transmission attempts (Req 7.7) or being discarded when the close
	// budget expired (Req 9.7).
	reasonAbandonedBatch
	// reasonNonRetryable is a drop caused by an ingest response classified as
	// non-retryable (Req 7.8, 7.10, 7.11, 7.15).
	reasonNonRetryable
	// reasonConversionPanic is a drop caused by a recovered panic while
	// converting a Log_Record into a Log_Event (Req 8.5).
	reasonConversionPanic
	// reasonAfterClose is a drop caused by an enqueue arriving after the close
	// operation returned (Req 9.11).
	reasonAfterClose
	// numDropReasons is the number of defined drop reasons. It is not itself a
	// reason; it sizes the per-reason array.
	numDropReasons
)

// Snapshot is a single consistent observation of the Log_Shipper's counters
// (Req 11.3, 11.4). Every field is copied under one acquisition of the counters
// mutex, so the accounting identity
//
//	Enqueued == Delivered + Dropped + InFlight + QueueDepth
//
// holds for every returned Snapshot, and the sum of DroppedByReason equals
// Dropped.
type Snapshot struct {
	// Enqueued counts admitted Log_Events: every Log_Record that passed the
	// level filter and for which shipping was attempted, counted before
	// conversion. Counting admissions rather than successful channel sends is
	// what makes the accounting identity hold on the drop-before-enqueue paths.
	Enqueued uint64
	// Delivered counts Log_Events accepted by the ingest endpoint (Req 7.1).
	Delivered uint64
	// Dropped counts Log_Events that reached a terminal state without being
	// delivered. It equals the sum of DroppedByReason.
	Dropped uint64
	// ConversionPanics counts recovered Log_Record conversion panics (Req 8.5).
	ConversionPanics uint64
	// RetriedBatches counts Delivery_Batch retransmissions (Req 7.2, 7.3).
	RetriedBatches uint64
	// AbandonedBatches counts Delivery_Batches discarded after exhausting their
	// transmission attempts (Req 7.7).
	AbandonedBatches uint64
	// DroppedByReason holds one counter per DropReason, indexed by DropReason.
	DroppedByReason [numDropReasons]uint64
	// QueueDepth is the number of Log_Events resident in the Event_Queue. It is
	// derived by subtraction inside the same lock acquisition as the other
	// fields rather than read from len(chan), because a channel length read
	// cannot be made atomic with the counter reads.
	QueueDepth uint64
	// InFlight is the number of Log_Events removed from the Event_Queue whose
	// Delivery_Batch has not yet reached a terminal state (Req 11.3).
	InFlight uint64
}

// counters is the unexported internal counter holder. All state is guarded by a
// single mutex; snapshot takes that mutex once and copies every field, which is
// the consistent observation Req 11.4 requires and which independent
// atomic.Uint64 values cannot provide.
//
// Locking discipline: every method takes this lock, mutates plain fields, and
// returns. It is never held across a channel send, an HTTP call, a clock call,
// or a diagnostic write. Shipper.lifecycleMu may be held while this lock is
// acquired for one admission update, but the reverse ordering is never used, so
// there is no lock cycle.
type counters struct {
	mu sync.Mutex
	// all fields below guarded by mu
	enqueued, delivered, dropped         uint64
	conversionPanics, retried, abandoned uint64
	inFlight                             uint64
	byReason                             [numDropReasons]uint64
}

// addSat adds n to *dst, holding at math.MaxUint64 instead of wrapping, so that
// a saturated counter never decreases and every subsequent increment completes
// without panicking (Req 11.1, 11.2).
func addSat(dst *uint64, n uint64) {
	if *dst > math.MaxUint64-n {
		*dst = math.MaxUint64
		return
	}
	*dst += n
}

// subFloor returns a - b, clamped at 0 instead of wrapping. uint64 subtraction
// that goes negative wraps to an absurd value near math.MaxUint64, so every
// derived subtraction in this file goes through subFloor.
func subFloor(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// addAdmitted records n Log_Events admitted for shipping. It is called once per
// record before conversion, so that a conversion panic still balances against an
// already-counted admission.
func (c *counters) addAdmitted(n uint64) {
	c.mu.Lock()
	addSat(&c.enqueued, n)
	c.mu.Unlock()
}

// addDelivered records n Log_Events accepted by the ingest endpoint.
func (c *counters) addDelivered(n uint64) {
	c.mu.Lock()
	addSat(&c.delivered, n)
	c.mu.Unlock()
}

// addDropped records n Log_Events dropped for the given reason, incrementing
// both the total and the matching per-reason counter under one lock acquisition
// so the Req 11.5 sum equality holds in every Snapshot.
//
// A reason outside the defined range can only arise from a bug in this package.
// Rather than skip the increment, which would break the Req 11.4 accounting
// identity, or index out of range, which would panic, such a drop is attributed
// to reasonNonRetryable so that both identities continue to hold.
func (c *counters) addDropped(reason DropReason, n uint64) {
	if reason < 0 || reason >= numDropReasons {
		reason = reasonNonRetryable
	}
	c.mu.Lock()
	addSat(&c.dropped, n)
	addSat(&c.byReason[reason], n)
	c.mu.Unlock()
}

// addInFlight adjusts the in-flight Log_Event count by delta. Positive deltas
// saturate at math.MaxUint64; negative deltas clamp at 0 so the count never
// wraps.
func (c *counters) addInFlight(delta int64) {
	if delta == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if delta > 0 {
		addSat(&c.inFlight, uint64(delta))
		return
	}
	// Negating math.MinInt64 overflows int64, so compute the magnitude in
	// uint64 space.
	mag := uint64(-(delta + 1)) + 1
	c.inFlight = subFloor(c.inFlight, mag)
}

// addConversionPanic records one recovered Log_Record conversion panic
// (Req 8.5). The corresponding dropped Log_Event is recorded separately through
// addDropped with reasonConversionPanic.
func (c *counters) addConversionPanic() {
	c.mu.Lock()
	addSat(&c.conversionPanics, 1)
	c.mu.Unlock()
}

// addRetriedBatch records one Delivery_Batch retransmission (Req 7.2, 7.3).
func (c *counters) addRetriedBatch() {
	c.mu.Lock()
	addSat(&c.retried, 1)
	c.mu.Unlock()
}

// addAbandonedBatch records one Delivery_Batch abandoned after exhausting its
// transmission attempts (Req 7.7). The Log_Events it carried are recorded
// separately through addDropped with reasonAbandonedBatch.
func (c *counters) addAbandonedBatch() {
	c.mu.Lock()
	addSat(&c.abandoned, 1)
	c.mu.Unlock()
}

// snapshot copies every counter under a single lock acquisition and derives
// QueueDepth as enqueued - delivered - dropped - inFlight, rather than reading
// len(chan), because a channel length read cannot be made atomic with the
// counter reads (Req 11.3, 11.4).
//
// The subtraction is performed as successive floored subtractions. That avoids
// overflow while summing the three subtrahends and clamps the result at 0
// instead of wrapping to a value near math.MaxUint64. Underflow is unreachable
// given the counting discipline, and is only possible once a counter saturates
// at math.MaxUint64, at which point the accounting identity can no longer hold
// arithmetically; clamping keeps the reported depth plausible in that case.
//
// It is safe to call on a disabled Log_Shipper: it mutates nothing and returns
// the zero Snapshot when no counter has ever been incremented (Req 11.10, 11.11).
func (c *counters) snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := Snapshot{
		Enqueued:         c.enqueued,
		Delivered:        c.delivered,
		Dropped:          c.dropped,
		ConversionPanics: c.conversionPanics,
		RetriedBatches:   c.retried,
		AbandonedBatches: c.abandoned,
		DroppedByReason:  c.byReason,
		InFlight:         c.inFlight,
	}
	s.QueueDepth = subFloor(subFloor(subFloor(c.enqueued, c.delivered), c.dropped), c.inFlight)
	return s
}
