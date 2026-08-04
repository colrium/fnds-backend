package logging

// Property 7: Queue capacity is never exceeded.
//
// *For any* burst of concurrent enqueue operations against *any* configured
// capacity, the number of Log_Events resident in the Event_Queue never exceeds
// that capacity at any observation point (Req 6.2), because an enqueue that
// arrives at capacity discards its event instead of growing the queue (Req 6.3).
//
// The property is narrowed exactly as the design's reflection note requires: it
// is purely the capacity bound. It asserts nothing about drop attribution or the
// accounting identity — that is P13's job — so a failure here localizes to the
// queue rather than to the counters.
//
// Two observation channels, and why both are needed:
//
//  1. **`len(s.queue)`**, sampled between sends from inside the producing
//     goroutines and from a sampler goroutine spinning alongside them. A channel
//     length read is racy as an *equality* claim but sound as an *upper-bound*
//     claim: it can never report more elements than the buffer holds. That makes
//     it the right instrument for a property whose whole content is an upper
//     bound, and it is the instrument the example-based concurrency test in
//     shipper_test.go already uses.
//
//  2. **`Snapshot().QueueDepth`**, the depth the health snapshot derives by
//     subtraction rather than by reading the channel. This is what an operator
//     and Req 11.3 actually see, so a capacity bound that held for the channel
//     but not for the reported depth would still be a defect.
//
// The two are bounded differently *while producers are running*, and the
// difference is a property of the counting discipline rather than slack added to
// quiet a flake. Handle counts an admission **before** conversion and enqueue
// (see handler.go), and `QueueDepth` is derived as
// `enqueued - delivered - dropped - inFlight`, so an event that has been admitted
// but has not yet reached its send-or-drop is counted in the derived depth while
// being resident nowhere. With P concurrent producers, at most P such admissions
// are in flight at once, plus at most one event that the single delivery
// goroutine has received but not yet counted as in-flight. So mid-burst the
// derived depth is bounded by `capacity + P + 1`, not by `capacity`.
//
// That transient is unobservable once the producers quiesce: with no further
// sends, every admission has reached a terminal state or the channel, and each
// uncounted removal has freed the slot it came from, so
// `derived == resident + pendingRemovals <= capacity` exactly. The runner
// therefore asserts the strict `<= capacity` bound on the derived depth at
// quiescence, and — for the non-draining case, where the arithmetic is exact —
// pins it to `min(offered, capacity)`. That last assertion is what gives the
// property teeth: it fails both if the queue ever held more than the configured
// capacity and if it silently held less, which is how a mis-sized queue or a
// capacity read from the wrong field would show up.
//
// Both the "nothing draining" and the "delivery goroutine draining concurrently"
// cases are generated. The second is the one where a capacity violation would be
// a genuine race rather than a counting error, since sends and receives on the
// same buffer overlap there.

import (
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// p7MaxAttempts caps the total offered events per iteration. It is small on
	// purpose: 100 iterations under -race must stay inside the package's
	// wall-clock budget (Req 12.2), and the property is about a boundary, which
	// is reached by offering a little more than the capacity rather than by
	// offering a lot.
	p7MaxAttempts = 160
	// p7MaxProducers caps concurrent producers per iteration.
	p7MaxProducers = 8
)

// p7Burst is one generated input: a configured capacity, a producer count, a
// per-producer send count, whether the delivery goroutine is draining
// concurrently, and whether producers reach enqueue through the real
// Better_Stack_Handler.
//
// Every field is exported so pbtFailf can render the input as JSON.
type p7Burst struct {
	// Capacity is the configured Event_Queue capacity (Req 6.1).
	Capacity int
	// Producers is the number of goroutines enqueueing concurrently.
	Producers int
	// PerProducer is the number of events each producer offers.
	PerProducer int
	// Draining is true when the Shipper is started, so the single delivery
	// goroutine consumes the queue while the producers fill it. This is the case
	// where a capacity violation would be a race on the buffer rather than a
	// counting error.
	Draining bool
	// ViaHandler is true when producers emit records through slog into the
	// Better_Stack_Handler, which is the production path and the one that counts
	// an admission before enqueue. When false, producers mirror that discipline
	// directly — addAdmitted then enqueue — with one already-converted event,
	// which exercises the same counting at a fraction of the conversion cost.
	ViaHandler bool
}

// Offered is the total number of events the burst offers to the queue.
func (b p7Burst) Offered() int { return b.Producers * b.PerProducer }

// Overflows reports whether the burst offers more events than the queue can
// hold, which is the case that actually exercises the bound.
func (b p7Burst) Overflows() bool { return b.Offered() > b.Capacity }

// p7GenBurst draws a burst, biased toward the boundaries where a capacity bug
// would live: capacity 1 and 2, a single producer, and offered counts just above
// the capacity.
func p7GenBurst(r *rand.Rand) p7Burst {
	b := p7Burst{
		Capacity:   p7GenCapacity(r),
		Producers:  p7GenProducers(r),
		Draining:   pbtChance(r, 50),
		ViaHandler: pbtChance(r, 35),
	}
	b.PerProducer = p7GenPerProducer(r, b.Capacity, b.Producers)
	return b
}

// p7GenCapacity draws a configured capacity in the range ResolveConfig accepts
// (1..100000), weighted toward the smallest values because that is where the
// distinction between "at capacity" and "one below capacity" is reachable within
// a small burst.
func p7GenCapacity(r *rand.Rand) int {
	switch {
	case pbtChance(r, 55):
		return pbtPick(r, []int{1, 1, 2, 2, 3, 4, 5, 8})
	case pbtChance(r, 70):
		return 1 + r.Intn(16)
	default:
		// Larger than any burst can fill, which exercises the bound from the
		// other side: a queue that is never full must never drop, and the exact
		// non-draining assertion below pins the resident count to the offered
		// count in that case.
		return 64 + r.Intn(240)
	}
}

// p7GenProducers draws a producer count, including 1 so the sequential case is
// covered by the same property.
func p7GenProducers(r *rand.Rand) int {
	if pbtChance(r, 25) {
		return pbtPick(r, []int{1, 1, 2})
	}
	return 1 + r.Intn(p7MaxProducers)
}

// p7GenPerProducer draws a per-producer send count, biased so most bursts push
// past the capacity while some stay below it, and clamped so no iteration offers
// more than p7MaxAttempts events in total.
func p7GenPerProducer(r *rand.Rand, capacity, producers int) int {
	var per int
	switch {
	case pbtChance(r, 20):
		// Fewer events than the capacity: the queue never fills.
		per = 1 + r.Intn(1+capacity/2)
	case pbtChance(r, 50):
		// Just past the capacity, where the first drop happens.
		per = capacity/producers + 1
	default:
		// Comfortably past the capacity, so every producer sees a full queue.
		per = 2*capacity/producers + 2
	}
	if per < 1 {
		per = 1
	}
	if max := p7MaxAttempts / producers; per > max {
		per = max
	}
	if per < 1 {
		per = 1
	}
	return per
}

// p7Observations accumulates the maximum resident count and the maximum derived
// depth seen across every observation point, from every goroutine.
//
// Maxima rather than a running log: the property is a bound, so the only
// interesting summary of a million samples is the largest one, and recording it
// with a CAS loop keeps the sampling cheap enough to run in a tight loop without
// serializing the producers behind the test's own bookkeeping.
type p7Observations struct {
	maxResident atomic.Int64
	maxDerived  atomic.Int64
	samples     atomic.Int64
}

// record notes one observation point: the channel length and the derived depth
// observed there.
func (o *p7Observations) record(resident int, derived uint64) {
	p7RecordMax(&o.maxResident, int64(resident))
	p7RecordMax(&o.maxDerived, p7ToInt64(derived))
	o.samples.Add(1)
}

// MaxResident returns the largest len(queue) observed.
func (o *p7Observations) MaxResident() int64 { return o.maxResident.Load() }

// MaxDerived returns the largest Snapshot().QueueDepth observed.
func (o *p7Observations) MaxDerived() int64 { return o.maxDerived.Load() }

// Samples returns the number of observation points recorded, which the runner
// asserts is non-zero so a broken sampling loop cannot make the property vacuous.
func (o *p7Observations) Samples() int64 { return o.samples.Load() }

// p7RecordMax raises *dst to v if v is larger, with a CAS loop because several
// producers and the sampler write concurrently.
func p7RecordMax(dst *atomic.Int64, v int64) {
	for {
		current := dst.Load()
		if v <= current || dst.CompareAndSwap(current, v) {
			return
		}
	}
}

// p7ToInt64 converts a counter value for comparison, clamping at MaxInt64 so an
// absurd value from a saturated counter is reported rather than wrapping
// negative and silently passing an upper-bound check.
func p7ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// p7DerivedAllowance is the number of admissions that can be counted in the
// derived depth while resident nowhere: one per producer that is between
// addAdmitted and its send-or-drop, plus one for an event the single delivery
// goroutine has received but not yet counted as in-flight.
//
// It applies only to observations taken while producers are running. At
// quiescence the allowance is zero, which is what the runner asserts.
func p7DerivedAllowance(in p7Burst) int64 { return int64(in.Producers) + 1 }

// p7ResidencyViolation returns a description of the first capacity violation the
// observations show, or the empty string when the bound held.
//
// It is a pure function over the recorded maxima so that the guard itself is
// testable: TestP7ResidencyViolationDetectsAnOverCapacityObservation feeds it a
// synthetic over-capacity observation and asserts it fires, which is what keeps
// a green property run from being evidence of nothing.
func p7ResidencyViolation(in p7Burst, obs *p7Observations) string {
	if got := obs.MaxResident(); got > int64(in.Capacity) {
		return fmt.Sprintf("observed len(queue) = %d, want at most the configured capacity %d (Req 6.2)",
			got, in.Capacity)
	}
	if bound := int64(in.Capacity) + p7DerivedAllowance(in); obs.MaxDerived() > bound {
		return fmt.Sprintf("observed Snapshot().QueueDepth = %d mid-burst, want at most capacity+producers+1 = %d",
			obs.MaxDerived(), bound)
	}
	if obs.Samples() == 0 {
		return "recorded zero observation points: the property observed nothing"
	}
	return ""
}

// Feature: betterstack-error-logging, Property 7: Queue capacity is never exceeded
// **Validates: Requirements 6.2, 6.3**
func TestProperty7_QueueCapacityIsNeverExceeded(t *testing.T) {
	r, seed := pbtRand(t)
	baseline := stableGoroutineCount()

	var (
		sawCapacityOne   bool
		sawOverflow      bool
		sawDraining      bool
		sawNotDraining   bool
		sawHandlerPath   bool
		sawUnderCapacity bool
	)

	for i := 0; i < pbtIterations; i++ {
		in := p7GenBurst(r)

		sawCapacityOne = sawCapacityOne || in.Capacity == 1
		sawOverflow = sawOverflow || in.Overflows()
		sawUnderCapacity = sawUnderCapacity || !in.Overflows()
		sawDraining = sawDraining || in.Draining
		sawNotDraining = sawNotDraining || !in.Draining
		sawHandlerPath = sawHandlerPath || in.ViaHandler

		p7RunBurst(t, seed, i, in)
	}

	// Generator coverage. A property that never offered more events than the
	// capacity, or never ran the concurrent-drain case, would pass while proving
	// nothing, and with no shrinking there is nothing else to notice that from.
	for _, c := range []struct {
		saw  bool
		what string
	}{
		{sawCapacityOne, "capacity 1"},
		{sawOverflow, "a burst larger than the capacity"},
		{sawUnderCapacity, "a burst smaller than the capacity"},
		{sawDraining, "the concurrently draining case"},
		{sawNotDraining, "the nothing-draining case"},
		{sawHandlerPath, "the Better_Stack_Handler path"},
	} {
		if !c.saw {
			t.Errorf("generator produced no iteration covering %s (seed %d)", c.what, seed)
		}
	}

	// No iteration may leak its delivery goroutine into the rest of the package.
	if !waitFor(func() bool { return runtime.NumGoroutine() <= baseline }, 2*time.Second) {
		t.Errorf("goroutine count = %d after %d iterations, want at most the baseline %d",
			runtime.NumGoroutine(), pbtIterations, baseline)
	}
}

// p7RunBurst runs one generated burst and checks the capacity bound.
func p7RunBurst(t *testing.T, seed int64, iteration int, in p7Burst) {
	t.Helper()

	cfg, _ := newTestShipperConfig(t, true, in.Capacity)
	s := NewShipper(cfg)

	// The configured capacity must be the channel's capacity, or every bound
	// below would be checked against the wrong number (Req 6.1, 6.2).
	if got := cap(s.queue); got != in.Capacity {
		pbtFatalf(t, seed, iteration, in, "cap(queue) = %d, want the configured capacity %d", got, in.Capacity)
	}

	if in.Draining {
		s.Start() // the single delivery goroutine consumes while producers fill
		defer close(s.closeDone)
	}

	// Producers reach enqueue either through the real handler or through the
	// admission-then-enqueue pair Handle performs, so the derived depth means the
	// same thing in both modes.
	logger := slog.New(s.Handler())
	shared := testEvent(t, "p7 burst")

	obs := &p7Observations{}
	producersDone := make(chan struct{})
	samplerDone := make(chan struct{})

	// A sampler alongside the producers, so observation points are not limited to
	// the instants a producer happens to reach. This is the tight loop the design
	// calls for.
	go func() {
		defer close(samplerDone)
		for {
			select {
			case <-producersDone:
				return
			default:
			}
			obs.record(len(s.queue), s.Snapshot().QueueDepth)
			runtime.Gosched()
		}
	}()

	var wg sync.WaitGroup
	for p := 0; p < in.Producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < in.PerProducer; i++ {
				if in.ViaHandler {
					logger.Error("p7 burst", slog.Int("i", i))
				} else {
					s.counters.addAdmitted(1) // what Handle counts before conversion
					s.enqueue(shared)
				}
				// An observation point between every send.
				obs.record(len(s.queue), s.Snapshot().QueueDepth)
			}
		}()
	}
	wg.Wait()
	close(producersDone)
	<-samplerDone

	if violation := p7ResidencyViolation(in, obs); violation != "" {
		pbtFailf(t, seed, iteration, in, "%s", violation)
	}

	if in.Draining {
		// One transient outlives producer quiescence, and it is the delivery
		// goroutine's, not a producer's: `run` can have received an event and not
		// yet counted it as in-flight, while a producer has already refilled the
		// slot that receive freed. The derived depth then reads capacity+1 —
		// observed at capacity 1 with a single producer — even though the channel
		// holds exactly one event, because the uncounted removal is still inside
		// the `enqueued - delivered - dropped - inFlight` subtraction.
		//
		// So the strict bound is asserted once that single removal is accounted
		// for, rather than being loosened to capacity+1 forever: waiting costs at
		// most one poll step in practice, and a derived depth that stayed above
		// the capacity — which is what an operator would see — still fails.
		settled := waitFor(func() bool {
			return p7ToInt64(s.Snapshot().QueueDepth) <= int64(in.Capacity) && len(s.queue) <= in.Capacity
		}, 500*time.Millisecond)
		if !settled {
			snap := s.Snapshot()
			pbtFailf(t, seed, iteration, in,
				"Snapshot().QueueDepth = %d and len(queue) = %d did not settle to at most the capacity %d (%+v)",
				snap.QueueDepth, len(s.queue), in.Capacity, snap)
		}
		// Nothing exact to say beyond that: the delivery goroutine removes events
		// on its own schedule, so the resident count after the burst is any value
		// in 0..capacity, and the bounds are the whole claim.
		return
	}

	// Nothing drained, so no removal transient exists and the queue filled and
	// stayed full: residency is exactly min(offered, capacity). This is the
	// assertion that fails if the queue ever held more than the configured
	// capacity *or* less, which is how a mis-sized buffer or a capacity taken from
	// the wrong field surfaces.
	snap := s.Snapshot()
	want := in.Offered()
	if want > in.Capacity {
		want = in.Capacity
	}
	if got := len(s.queue); got != want {
		pbtFailf(t, seed, iteration, in, "len(queue) = %d after the burst, want min(offered, capacity) = %d", got, want)
	}
	if got := snap.QueueDepth; got != uint64(want) {
		pbtFailf(t, seed, iteration, in,
			"Snapshot().QueueDepth = %d after the burst, want min(offered, capacity) = %d (%+v)", got, want, snap)
	}
}

// TestP7ResidencyViolationDetectsAnOverCapacityObservation is the property's own
// oracle test: it proves the guard fires on an over-capacity observation, so a
// green Property 7 run is evidence about the queue rather than evidence that the
// check is unreachable.
func TestP7ResidencyViolationDetectsAnOverCapacityObservation(t *testing.T) {
	in := p7Burst{Capacity: 4, Producers: 2, PerProducer: 10}

	within := &p7Observations{}
	within.record(4, 4)
	within.record(0, uint64(int64(in.Capacity)+p7DerivedAllowance(in)))
	if got := p7ResidencyViolation(in, within); got != "" {
		t.Errorf("p7ResidencyViolation on in-bounds observations = %q, want no violation", got)
	}

	tooResident := &p7Observations{}
	tooResident.record(in.Capacity+1, 0)
	if got := p7ResidencyViolation(in, tooResident); got == "" {
		t.Error("p7ResidencyViolation on a resident count above the capacity = no violation, want a violation")
	}

	tooDerived := &p7Observations{}
	tooDerived.record(0, uint64(int64(in.Capacity)+p7DerivedAllowance(in)+1))
	if got := p7ResidencyViolation(in, tooDerived); got == "" {
		t.Error("p7ResidencyViolation on a derived depth above capacity+producers+1 = no violation, want a violation")
	}

	if got := p7ResidencyViolation(in, &p7Observations{}); got == "" {
		t.Error("p7ResidencyViolation with zero samples = no violation, want a violation")
	}
}

// TestP7GeneratorStaysWithinItsBudgets pins the generator's contract: every drawn
// burst is runnable (positive counts, a capacity ResolveConfig accepts) and stays
// inside the per-iteration work budget, so the property cannot become slow or
// invalid through a later edit to the drawing weights.
func TestP7GeneratorStaysWithinItsBudgets(t *testing.T) {
	r, seed := pbtRand(t)

	for i := 0; i < pbtIterations; i++ {
		in := p7GenBurst(r)
		switch {
		case in.Capacity < 1 || in.Capacity > 100000:
			pbtFailf(t, seed, i, in, "Capacity = %d, want 1..100000 (the range ResolveConfig accepts)", in.Capacity)
		case in.Producers < 1 || in.Producers > p7MaxProducers:
			pbtFailf(t, seed, i, in, "Producers = %d, want 1..%d", in.Producers, p7MaxProducers)
		case in.PerProducer < 1:
			pbtFailf(t, seed, i, in, "PerProducer = %d, want at least 1", in.PerProducer)
		case in.Offered() > p7MaxAttempts:
			pbtFailf(t, seed, i, in, "Offered() = %d, want at most %d", in.Offered(), p7MaxAttempts)
		}
	}
}
