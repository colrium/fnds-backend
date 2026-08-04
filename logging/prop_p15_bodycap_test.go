package logging

// Property 15: Delivery_Batch bodies stay under the 1 MiB cap.
//
// *For any* sequence of Log_Events of arbitrary sizes and *any* maximum
// Delivery_Batch size, every request body received by the test server occupies at
// most 1048576 bytes and contains at least one Log_Event (Req 5.14, 6.6).
//
// The property is narrowed exactly as the design's reflection note requires: it
// is purely the body-size and non-emptiness invariant. It deliberately asserts
// **nothing** about the multiset of delivered events — that is Property 6's claim,
// and duplicating it here would mean a batching bug failed two properties and
// localized to neither. What it does assert beyond the two size clauses is that
// nothing was *dropped*, which is a different claim: Req 5.14 says an event that
// does not fit is retained for the next Delivery_Batch, never discarded, so a
// body cap implemented by throwing the offending event away would satisfy the
// size bound and fail here.
//
// Why the non-emptiness clause carries its weight: the size bound alone is
// satisfied by a Shipper that transmits nothing at all, and — more subtly — by
// one that answers "does not fit" for an event arriving at an *empty* pending
// batch. That off-by-one transmits an empty body, leaves the event pending, and
// refuses it again forever: an infinite stream of `[]` bodies, every one of them
// comfortably under the cap. `fits` returns true for an empty pending precisely
// to prevent it, and this is the property that would notice if it stopped.
//
// Three deliberate choices in the harness below:
//
//   - **Events are enqueued directly**, with the admission counted the way Handle
//     counts it, rather than emitted through a logger. The Handle path runs
//     capEventSize, which caps every event at 65536 bytes (Req 3.12), so reaching
//     a 1048576-byte body through it would take ~16 events per body and would
//     never reach the interesting case at all — an event that cannot fit under
//     the cap even on its own. deliveryPaddedEvent skips capEventSize for exactly
//     this reason.
//
//   - **The tail is flushed by advancing the fakeClock.** Events that reach
//     neither the BatchSize trigger nor the body cap leave only on a flush tick
//     (Req 5.7). Forget that, and the last partial batch never reaches the server
//     and every size assertion holds vacuously. This is the likeliest source of a
//     false failure in this property, so the runner waits for the queue to drain,
//     advances the clock, and then requires that every admitted event reached a
//     terminal state before it inspects a single body.
//
//   - **The server answers 202 for every request**, so the delivery-outcome
//     classification (tasks 8.1 through 8.3) cannot influence what this property
//     observes. It is about body sizes, not about status handling.
//
// Iteration cost is the one thing this file has to manage. A body that approaches
// the cap costs a megabyte of payload by definition, so a run in which every
// iteration did that would move tens of megabytes and blow the package's 5-second
// wall-clock budget (Req 12.2). The resolution is not fewer iterations — the
// harness's standard is that no property runs fewer than pbtIterations — but a
// *budgeted mixture*: most iterations draw tiny-to-tens-of-KiB events, and at
// most p15MaxLargeIterations of them draw the near-cap and over-cap shapes.
// Coverage of those two shapes is then guaranteed rather than hoped for: the loop
// forces each one on a reserved late iteration if the draws never produced it.

import (
	"fmt"
	"math/rand"
	"net/http"
	"runtime"
	"testing"
)

// The generated size classes. Each name says what it is for, because the choice
// of constant is what makes a case reachable at all.
const (
	// p15TinyBytes bounds the "tiny" class. deliveryPaddedEvent produces at
	// least ~200 bytes regardless, so this class is really "the smallest event
	// there is", which is what makes the count trigger and the cap trigger
	// interact rather than the cap trigger dominating.
	p15TinyBytes = 256

	// p15StraddleBytes is sized so that two such events cannot share a body:
	// 2 x 556672 bytes is above the 1048576-byte cap, while one of them alone is
	// comfortably below it. This is the case the cap exists for — a legitimate
	// split — as opposed to the pathological single oversized event below.
	p15StraddleBytes = maxBatchBodyBytes/2 + 32*1024

	// p15OversizedBytes exceeds the whole body cap on its own. No event reaching
	// the delivery goroutine through Handle can be this large (capEventSize caps
	// at 65536), so it is unreachable in production — but it is the input that
	// distinguishes "a batch always holds at least one event" from "an event
	// that does not fit is refused forever", and refusing it forever is a real
	// failure mode of a cap check written with the wrong boundary.
	p15OversizedBytes = maxBatchBodyBytes + 16*1024

	// p15MaxEvents caps the events per iteration. Small on purpose: the property
	// is about a byte boundary, which is reached by sizing events rather than by
	// enqueueing many of them.
	p15MaxEvents = 8

	// p15MaxLargeIterations caps how many iterations may draw a near-cap or
	// over-cap shape. Each of those moves about 1.1 MiB of payload, so this is
	// the constant that keeps the run inside the package's wall-clock budget
	// (Req 12.2) while every other iteration stays in the tens of KiB.
	p15MaxLargeIterations = 3
)

// The generated shapes, as strings so that a counterexample names its shape
// instead of printing an enum ordinal.
const (
	// p15ShapeSmall draws every event from the tiny/KiB/tens-of-KiB classes. No
	// single body approaches the cap; the batch boundaries come from BatchSize.
	p15ShapeSmall = "small"
	// p15ShapeStraddle plants two p15StraddleBytes events, so a body cap split
	// is forced whenever they land in the same pending batch.
	p15ShapeStraddle = "straddle"
	// p15ShapeOversized plants one event larger than the entire cap.
	p15ShapeOversized = "oversized"
)

// p15Input is one generated input: a maximum Delivery_Batch size and the
// serialized sizes of the events to enqueue, in enqueue order.
//
// Every field is exported so pbtFailf can render the input as JSON.
type p15Input struct {
	// Shape names which size mixture produced Sizes, so a counterexample says
	// which case it came from.
	Shape string
	// BatchSize is the configured maximum Delivery_Batch size (Req 5.4). It is
	// varied because the count trigger and the cap trigger interact: with
	// BatchSize 1 the cap can never fire, and with a large BatchSize it is the
	// only thing that splits a batch.
	BatchSize int
	// Sizes is the minimum serialized size of each event, in enqueue order.
	Sizes []int
}

// Events reports how many events this input enqueues.
func (in p15Input) Events() int { return len(in.Sizes) }

// TotalBytes reports the padding this input generates, which is what the budget
// test asserts against.
func (in p15Input) TotalBytes() int {
	total := 0
	for _, size := range in.Sizes {
		total += size
	}
	return total
}

// p15GenInputs draws the inputs for a whole run.
//
// The whole sequence is drawn up front rather than one input per iteration for
// two reasons: the large-shape allowance is a property of the run rather than of
// an input, and drawing the sequence as a value lets
// TestP15GeneratorStaysWithinItsByteBudget assert the run's byte budget over
// exactly the inputs the property will execute.
//
// Drawing a size list is cheap — no Event is built here — so this costs nothing
// beyond the slices.
func p15GenInputs(r *rand.Rand) []p15Input {
	inputs := make([]p15Input, 0, pbtIterations)

	var (
		large         int
		sawStraddle   bool
		sawOversized  bool
		straddleForce = pbtIterations - 2
		oversizeForce = pbtIterations - 1
	)
	for i := 0; i < pbtIterations; i++ {
		// Reserved late iterations guarantee both expensive shapes appear even
		// on a seed whose draws never chose them. With no shrinking to lean on,
		// a property that silently covered only the cheap shape would pass while
		// proving nothing about the cap.
		forced := ""
		switch {
		case i == straddleForce && !sawStraddle:
			forced = p15ShapeStraddle
		case i == oversizeForce && !sawOversized:
			forced = p15ShapeOversized
		}

		in := p15GenInput(r, large < p15MaxLargeIterations, forced)
		switch in.Shape {
		case p15ShapeStraddle:
			sawStraddle = true
			large++
		case p15ShapeOversized:
			sawOversized = true
			large++
		}
		inputs = append(inputs, in)
	}
	return p15GuaranteeACapSplit(inputs)
}

// p15GuaranteeACapSplit ensures at least one straddle input has a BatchSize its
// event count cannot reach, so the two events that cannot share a body are
// certain to meet in the same pending batch and the *cap* trigger — not the count
// trigger — is what splits it.
//
// Without this, a run in which every straddle iteration happened to draw a small
// BatchSize would separate the two large events by the count trigger, and the
// property would observe only ordinary batching while still passing. That is the
// one way this property could go quietly vacuous about the thing it is named
// after, and it is cheap to close: one input's BatchSize is raised, every other
// draw is left alone, and the interaction between the two triggers is still
// generated everywhere else.
func p15GuaranteeACapSplit(inputs []p15Input) []p15Input {
	first := -1
	for i, in := range inputs {
		if in.Shape != p15ShapeStraddle {
			continue
		}
		if in.BatchSize >= in.Events() {
			return inputs // a cap split is already inevitable somewhere
		}
		if first < 0 {
			first = i
		}
	}
	if first >= 0 {
		inputs[first].BatchSize = inputs[first].Events()
	}
	return inputs
}

// p15GenInput draws one input. allowLarge is false once the run has spent its
// near-cap allowance, and forced overrides the draw so a reserved iteration gets
// the shape the run is still missing.
func p15GenInput(r *rand.Rand, allowLarge bool, forced string) p15Input {
	shape := p15ShapeSmall
	switch {
	case forced != "":
		shape = forced
	case !allowLarge:
		shape = p15ShapeSmall
	case pbtChance(r, 10):
		shape = p15ShapeStraddle
	case pbtChance(r, 6):
		shape = p15ShapeOversized
	}

	in := p15Input{Shape: shape, BatchSize: p15GenBatchSize(r)}
	in.Sizes = make([]int, 1+r.Intn(p15MaxEvents))
	for i := range in.Sizes {
		in.Sizes[i] = p15GenSmallSize(r)
	}

	switch shape {
	case p15ShapeStraddle:
		// Two of them, at distinct positions, so their relative order and the
		// small events between them vary.
		for len(in.Sizes) < 2 {
			in.Sizes = append(in.Sizes, p15GenSmallSize(r))
		}
		first := r.Intn(len(in.Sizes))
		second := (first + 1 + r.Intn(len(in.Sizes)-1)) % len(in.Sizes)
		in.Sizes[first] = p15StraddleBytes
		in.Sizes[second] = p15StraddleBytes
	case p15ShapeOversized:
		in.Sizes[r.Intn(len(in.Sizes))] = p15OversizedBytes
	}
	return in
}

// p15GenSmallSize draws an event size from the cheap classes: tiny, a few KiB,
// and tens of KiB up to the 65536-byte per-event cap of Req 3.12, which is the
// largest size the production Handle path can ever produce.
func p15GenSmallSize(r *rand.Rand) int {
	switch {
	case pbtChance(r, 60):
		return r.Intn(p15TinyBytes)
	case pbtChance(r, 65):
		return 1024 + r.Intn(7*1024)
	default:
		return 16*1024 + r.Intn(maxEventBytes-16*1024+1)
	}
}

// p15GenBatchSize draws a maximum Delivery_Batch size in the range ResolveConfig
// accepts (1..1000), weighted toward values a handful of events can reach.
//
// Both extremes matter. BatchSize 1 means every event is its own batch, so the
// cap trigger can never fire and the non-emptiness clause is carried entirely by
// single-event bodies. A BatchSize above the event count means the count trigger
// never fires, so every split that happens came from the cap.
func p15GenBatchSize(r *rand.Rand) int {
	if pbtChance(r, 40) {
		return pbtPick(r, []int{1, 2, 3, 4})
	}
	return 4 + r.Intn(12)
}

// p15Body is one recorded request body, reduced to the two facts the property is
// about: its byte length and how many Log_Events it carried.
type p15Body struct {
	Bytes  int
	Events int
}

// p15Violation returns a description of the first body that breaks the property,
// or the empty string when every body held.
//
// allowedLoneBodies is the exact set of body sizes an over-cap body is permitted
// to have: for each generated event too large to fit under the cap even on its
// own, the size of the body it produces alone. Such an event is still
// transmitted, in a batch of one, because Req 5.14 retains it rather than
// discarding it and a batch that refused it could never make progress — so its
// body is over the cap by the only unavoidable margin there is. The set is exact
// rather than a bound because a single-event body's size *is*
// batchBodyBytes(1, eventBytes(ev)) for that event, so membership admits nothing
// else. When no generated event is that large the set is empty and the rule below
// reduces to the plain "at most 1048576 bytes".
//
// It is a pure function over the recorded bodies so that the guard itself is
// testable: TestP15ViolationDetectsAnOverCapBody feeds it synthetic violations
// and asserts each one fires, which is what keeps a green property run from being
// evidence of nothing.
func p15Violation(bodies []p15Body, allowedLoneBodies map[int]bool) string {
	for i, body := range bodies {
		if body.Events == 0 {
			return fmt.Sprintf("request %d carried zero Log_Events in %d bytes, want at least one: a transmitted Delivery_Batch always holds at least one event (Req 5.14)",
				i, body.Bytes)
		}
		if body.Bytes <= maxBatchBodyBytes {
			continue
		}
		if body.Events > 1 {
			return fmt.Sprintf("request %d body was %d bytes carrying %d events, want at most %d: the batch was not split (Req 5.14)",
				i, body.Bytes, body.Events, maxBatchBodyBytes)
		}
		if !allowedLoneBodies[body.Bytes] {
			return fmt.Sprintf("request %d body was %d bytes carrying one event, above the %d-byte cap and matching no event that is oversized on its own (Req 5.14)",
				i, body.Bytes, maxBatchBodyBytes)
		}
	}
	return ""
}

// Feature: betterstack-error-logging, Property 15: Delivery_Batch bodies stay under the 1 MiB cap
// **Validates: Requirements 5.14, 6.6**
func TestProperty15_DeliveryBatchBodiesStayUnderTheCap(t *testing.T) {
	r, seed := pbtRand(t)

	// No iteration may leak its delivery goroutine into the rest of the package.
	// Registered before the loop on purpose: cleanups run last-in-first-out, so
	// this one runs *after* the per-iteration cleanups that close each Shipper and
	// each server. Registered afterwards it would run first and count 100 live
	// delivery goroutines that were about to be released.
	baseline := stableGoroutineCount()
	t.Cleanup(func() {
		if !waitFor(func() bool { return runtime.NumGoroutine() <= baseline }, deliveryWaitTimeout) {
			t.Errorf("goroutine count = %d after %d iterations, want at most the baseline %d",
				runtime.NumGoroutine(), pbtIterations, baseline)
		}
	})

	var (
		sawStraddle      bool
		sawOversized     bool
		sawNearCapBody   bool
		sawMultiEvent    bool
		sawLoneOversized bool
		sawBatchSizeOne  bool
		sawCapSplit      bool
	)

	for i, in := range p15GenInputs(r) {
		sawStraddle = sawStraddle || in.Shape == p15ShapeStraddle
		sawOversized = sawOversized || in.Shape == p15ShapeOversized
		sawBatchSizeOne = sawBatchSizeOne || in.BatchSize == 1

		bodies := p15Run(t, seed, i, in)

		// The count trigger alone produces floor(events/BatchSize) full batches
		// plus one flushed remainder, so any request beyond that came from the
		// body cap. This is the evidence that the property exercised the cap and
		// not merely ordinary batching.
		if len(bodies) > (in.Events()+in.BatchSize-1)/in.BatchSize {
			sawCapSplit = true
		}

		for _, body := range bodies {
			sawNearCapBody = sawNearCapBody || body.Bytes > maxBatchBodyBytes/2
			sawMultiEvent = sawMultiEvent || body.Events > 1
			sawLoneOversized = sawLoneOversized || (body.Events == 1 && body.Bytes > maxBatchBodyBytes)
		}
	}

	// Generator coverage. A run whose bodies all sat at a few KiB would pass
	// while saying nothing about a 1 MiB cap, and with no shrinking there is
	// nothing else to notice that from.
	for _, c := range []struct {
		saw  bool
		what string
	}{
		{sawStraddle, "an iteration with two events that cannot share a body"},
		{sawCapSplit, "a Delivery_Batch split by the body cap rather than by BatchSize"},
		{sawOversized, "an iteration with an event larger than the whole cap"},
		{sawNearCapBody, "a body above half the cap"},
		{sawMultiEvent, "a body carrying more than one event"},
		{sawLoneOversized, "a single-event body above the cap, which must still be transmitted"},
		{sawBatchSizeOne, "BatchSize 1, where the count trigger fires on every event"},
	} {
		if !c.saw {
			t.Errorf("generator produced no iteration covering %s (seed %d)", c.what, seed)
		}
	}
}

// p15Run enqueues one generated input, flushes it to the recording server, and
// checks the property against every body received. It returns the recorded
// bodies so the caller can accumulate generator coverage.
func p15Run(t *testing.T, seed int64, iteration int, in p15Input) []p15Body {
	t.Helper()

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	// QueueSize equals the event count, so no enqueue can be refused for a full
	// queue and a non-zero Dropped below can only mean the delivery path
	// discarded something (Req 6.3 versus Req 5.14).
	cfg := deliveryConfig(server.URL, in.BatchSize, in.Events(), clock)
	s := startDeliveryShipper(t, cfg, clock)

	// Build every event first, and record which of them cannot fit under the cap
	// even alone: those are the only bodies allowed above it.
	events := make([]*Event, 0, in.Events())
	allowedLoneBodies := make(map[int]bool)
	for i, size := range in.Sizes {
		ev := deliveryPaddedEvent(t, deliveryMessage("p15", i), size)
		events = append(events, ev)
		if lone := batchBodyBytes(1, eventBytes(ev)); lone > maxBatchBodyBytes {
			allowedLoneBodies[lone] = true
		}
	}

	for _, ev := range events {
		s.counters.addAdmitted(1) // what Handle counts before conversion
		s.enqueue(ev)
	}

	// Every event has left the Event_Queue, so whatever remains is pending in the
	// delivery goroutine's local batch. QueueDepth is derived by subtraction, so
	// zero here means exactly "every admission is delivered, dropped, or
	// in-flight" — there is nothing left for the flush below to race with.
	if !waitFor(func() bool { return s.Snapshot().QueueDepth == 0 }, deliveryWaitTimeout) {
		pbtFatalf(t, seed, iteration, in, "the Event_Queue did not drain within %s (%+v)",
			deliveryWaitTimeout, s.Snapshot())
	}

	// Flush the tail on the fake clock. Events that reached neither BatchSize nor
	// the body cap leave only on a flush tick (Req 5.7), so without this the last
	// partial batch never reaches the server and every assertion below would hold
	// vacuously. It is a loop because a tick that arrives while a transmission is
	// in progress is coalesced into the ticker's one-slot buffer (Req 5.15): one
	// advance is normally enough, and a second costs nothing in the passing case.
	// Advancing with an empty pending transmits nothing at all (Req 5.12), so the
	// extra advances cannot manufacture a request.
	drained := false
	for advances := 0; advances < 3 && !drained; advances++ {
		clock.Advance(cfg.FlushEvery)
		drained = waitFor(func() bool { return s.Snapshot().InFlight == 0 }, deliveryWaitTimeout)
	}
	if !drained {
		pbtFatalf(t, seed, iteration, in, "%d event(s) were still in flight after 3 flush intervals (%+v)",
			s.Snapshot().InFlight, s.Snapshot())
	}

	// Every request has been answered before InFlight reached zero — transmit
	// records the terminal outcome after Send returns, and the handler appends the
	// body before it writes the status — so the recording below is complete.
	sizes := server.bodySizes()
	batches := server.batches(t)
	if len(sizes) != len(batches) {
		pbtFatalf(t, seed, iteration, in, "the server recorded %d body sizes and %d bodies", len(sizes), len(batches))
	}
	bodies := make([]p15Body, len(sizes))
	for i := range sizes {
		bodies[i] = p15Body{Bytes: sizes[i], Events: len(batches[i])}
	}

	if len(bodies) == 0 {
		pbtFailf(t, seed, iteration, in, "the server received zero requests after %d event(s) were admitted and flushed",
			in.Events())
		return bodies
	}
	if violation := p15Violation(bodies, allowedLoneBodies); violation != "" {
		pbtFailf(t, seed, iteration, in, "%s", violation)
	}

	// Req 5.14 retains an event that does not fit; it never discards one. This is
	// not the multiset claim — that is Property 6's — but it is what distinguishes
	// a cap honoured by splitting from one honoured by throwing events away.
	snap := s.Snapshot()
	if snap.Dropped != 0 {
		pbtFailf(t, seed, iteration, in, "Dropped = %d, want 0: an event that does not fit is retained for the next Delivery_Batch (Req 5.14) (%+v)",
			snap.Dropped, snap)
	}
	if snap.Delivered != uint64(in.Events()) {
		pbtFailf(t, seed, iteration, in, "Delivered = %d, want the %d admitted event(s) (%+v)",
			snap.Delivered, in.Events(), snap)
	}
	return bodies
}

// TestP15ViolationDetectsAnOverCapBody is the property's own oracle test: it
// proves the guard fires on each way a body can break the invariant, so a green
// Property 15 run is evidence about the cap rather than evidence that the check
// is unreachable.
func TestP15ViolationDetectsAnOverCapBody(t *testing.T) {
	const oversizedLone = maxBatchBodyBytes + 1024
	allowed := map[int]bool{oversizedLone: true}

	within := []p15Body{
		{Bytes: 2, Events: 1},                        // the smallest body there is
		{Bytes: maxBatchBodyBytes, Events: 12},       // exactly at the cap
		{Bytes: oversizedLone, Events: 1},            // the unavoidable lone oversized event
		{Bytes: maxBatchBodyBytes - 1, Events: 1000}, // one byte under
	}
	if got := p15Violation(within, allowed); got != "" {
		t.Errorf("p15Violation on in-bounds bodies = %q, want no violation", got)
	}

	cases := []struct {
		name  string
		body  p15Body
		allow map[int]bool
	}{
		{name: "an empty body", body: p15Body{Bytes: 2, Events: 0}, allow: allowed},
		{name: "a multi-event body one byte over the cap", body: p15Body{Bytes: maxBatchBodyBytes + 1, Events: 2}, allow: allowed},
		{name: "a multi-event body at the allowed lone size", body: p15Body{Bytes: oversizedLone, Events: 2}, allow: allowed},
		{name: "a lone-event body over the cap that no input justifies", body: p15Body{Bytes: oversizedLone, Events: 1}, allow: nil},
		{name: "a lone-event body larger than the oversized event offered", body: p15Body{Bytes: oversizedLone + 1, Events: 1}, allow: allowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p15Violation([]p15Body{tc.body}, tc.allow); got == "" {
				t.Errorf("p15Violation on %+v = no violation, want a violation", tc.body)
			}
		})
	}
}

// TestP15GuaranteeACapSplitRaisesExactlyOneBatchSize pins the post-pass that
// keeps the property from going vacuous: it raises one straddle input's BatchSize
// when no straddle input could reach a cap split, and changes nothing when one
// already could.
func TestP15GuaranteeACapSplitRaisesExactlyOneBatchSize(t *testing.T) {
	straddle := func(batchSize, events int) p15Input {
		in := p15Input{Shape: p15ShapeStraddle, BatchSize: batchSize, Sizes: make([]int, events)}
		for i := range in.Sizes {
			in.Sizes[i] = p15StraddleBytes
		}
		return in
	}

	// Nothing can split by the cap: the first straddle input is raised.
	raised := p15GuaranteeACapSplit([]p15Input{
		{Shape: p15ShapeSmall, BatchSize: 1, Sizes: []int{0, 0, 0}},
		straddle(2, 5),
		straddle(1, 4),
	})
	if got, want := raised[1].BatchSize, 5; got != want {
		t.Errorf("first straddle BatchSize = %d, want it raised to its event count %d", got, want)
	}
	if got := raised[2].BatchSize; got != 1 {
		t.Errorf("second straddle BatchSize = %d, want it left at 1: only one input needs raising", got)
	}

	// One straddle input already reaches a cap split: nothing is touched.
	untouched := p15GuaranteeACapSplit([]p15Input{straddle(1, 3), straddle(9, 4)})
	if got := untouched[0].BatchSize; got != 1 {
		t.Errorf("BatchSize = %d, want 1: a run that already guarantees a cap split must not be rewritten", got)
	}

	// No straddle input at all: nothing to raise, and no panic.
	if got := p15GuaranteeACapSplit([]p15Input{{Shape: p15ShapeSmall, BatchSize: 3, Sizes: []int{0}}}); got[0].BatchSize != 3 {
		t.Errorf("BatchSize = %d, want 3", got[0].BatchSize)
	}
}

// TestP15GeneratorStaysWithinItsByteBudget pins the generator's contract against
// the two things that could silently break this property: an input the runner
// cannot execute, and a run that moves so much payload it breaks the package's
// wall-clock budget (Req 12.2).
//
// It asserts over the same sequence the property executes, so a later edit to the
// drawing weights cannot make the property slow without failing here first.
func TestP15GeneratorStaysWithinItsByteBudget(t *testing.T) {
	// Per input: two straddle events plus a full complement of tens-of-KiB
	// events, with room to spare. Per run: the small iterations at a few tens of
	// KiB each, plus the large allowance at about 1.1 MiB each.
	const (
		inputByteBudget = 2 << 20
		runByteBudget   = 12 << 20
	)

	r, seed := pbtRand(t)
	inputs := p15GenInputs(r)

	if len(inputs) != pbtIterations {
		t.Fatalf("p15GenInputs returned %d inputs, want %d", len(inputs), pbtIterations)
	}

	total := 0
	large := 0
	capSplitCertain := false
	for i, in := range inputs {
		total += in.TotalBytes()
		if in.Shape != p15ShapeSmall {
			large++
		}
		if in.Shape == p15ShapeStraddle && in.BatchSize >= in.Events() {
			// The count trigger cannot separate the two events that cannot share
			// a body, so the cap must split this iteration.
			capSplitCertain = true
		}
		switch {
		case in.Shape != p15ShapeSmall && in.Shape != p15ShapeStraddle && in.Shape != p15ShapeOversized:
			pbtFailf(t, seed, i, in, "Shape = %q, want one of the three generated shapes", in.Shape)
		case in.BatchSize < 1 || in.BatchSize > 1000:
			pbtFailf(t, seed, i, in, "BatchSize = %d, want 1..1000 (the range ResolveConfig accepts)", in.BatchSize)
		case in.Events() < 1:
			pbtFailf(t, seed, i, in, "Events() = %d, want at least 1", in.Events())
		case in.Shape == p15ShapeStraddle && in.Events() < 2:
			pbtFailf(t, seed, i, in, "the straddle shape drew %d event(s), want at least 2", in.Events())
		case in.TotalBytes() > inputByteBudget:
			pbtFailf(t, seed, i, in, "TotalBytes() = %d, want at most %d", in.TotalBytes(), inputByteBudget)
		}
	}

	// One forced iteration may exceed the allowance, which is the price of
	// guaranteed coverage; two would mean the allowance is not being applied.
	if large > p15MaxLargeIterations+1 {
		t.Errorf("the run drew %d near-cap iterations, want at most %d (seed %d)",
			large, p15MaxLargeIterations+1, seed)
	}
	if total > runByteBudget {
		t.Errorf("the run generates %d bytes of payload, want at most %d (seed %d)", total, runByteBudget, seed)
	}
	if !capSplitCertain {
		t.Errorf("no straddle input has BatchSize >= its event count (seed %d): p15GuaranteeACapSplit must leave at least one iteration the body cap is certain to split", seed)
	}
	t.Logf("p15 run generates %d bytes of payload across %d inputs, %d of them near-cap",
		total, len(inputs), large)
}
