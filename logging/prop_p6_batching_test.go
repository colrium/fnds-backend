package logging

// Property 6: Batching boundaries do not change the delivered multiset.
//
// *For any* sequence of Log_Events and *any* maximum Delivery_Batch size in the
// range 1 through 100, against a server that always returns 202, the multiset of
// Log_Events received across all request bodies equals the multiset enqueued.
//
// This is a **confluence** property, and that is the whole point of it. The batch
// size decides how events are *grouped* into requests — one request per event at
// size 1, one request for the lot at size 100 — and the claim is that grouping is
// the only thing it decides. Nothing is lost at a batch boundary, nothing is
// duplicated by one, and nothing is reordered inside a request. That is what makes
// BETTERSTACK_BATCH_SIZE safe to tune in production (Req 5.4): an operator changing
// it trades request count against latency, never delivery.
//
// So the assertions are deliberately asymmetric:
//
//   - The **multiset** of delivered messages is compared exactly, with
//     multiplicities (Req 5.5, 5.7, 7.1). A retransmitted batch shows up as a
//     count of 2, a lost tail as a count of 0.
//   - **Within-request** order is asserted to be enqueue order (Req 5.8), which
//     the design's P6 row covers.
//   - **Across-request grouping is not asserted at all.** Where the splits fall is
//     exactly what the batch size is allowed to change, and it also depends on how
//     far the producer got before the delivery goroutine drained the queue. Pinning
//     it would make this a timing test rather than a confluence test.
//
// The one thing that would make this property fail for an honest reason is the
// **tail**. A count of events below the batch-size trigger — which is every
// remainder, and the whole sequence whenever the count is under the batch size —
// sits in `pending` until the flush interval elapses (Req 5.7). The fake clock is
// therefore advanced explicitly, in a loop, until every event has reached a
// terminal state; without that the delivered multiset would legitimately be short
// and the failure would be the test's, not the Shipper's. See p6WaitDelivered.
//
// Cost control (Req 12.2): event counts are capped at p6MaxEvents and time is
// driven by the fake clock, never slept through, so 100 iterations — each with its
// own httptest server, Shipper, and delivery goroutine — cost milliseconds each.
//
// Every identifier here is `p6`-prefixed. The server, the Shipper construction,
// the emission, and the body decoding are all reused from delivery_test.go
// (newDeliveryServer, deliveryConfig, startDeliveryShipper, deliveryLogger,
// deliveryEmit, deliveryMessage, deliveryExpectedMessages) so that this property
// and the example-based delivery tests observe delivery through exactly the same
// instruments.

import (
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	// p6MaxEvents caps the events offered per iteration. Small on purpose: the
	// property is about boundaries, which are reached by crossing the batch-size
	// trigger a couple of times, not by shipping thousands of events. At batch
	// size 1 this is also the per-iteration request count, so it bounds the HTTP
	// work as well (Req 12.2).
	p6MaxEvents = 48

	// p6MaxBatchSize is the upper end of the range the property quantifies over.
	// ResolveConfig accepts 1..1000; the property stops at 100 because a batch
	// size above the event budget behaves identically to any other size above it
	// — the whole sequence leaves on one flush tick — and 100 already covers
	// that case.
	p6MaxBatchSize = 100

	// p6DupPool is the number of distinct messages the duplicate mode draws from,
	// so the enqueued multiset carries multiplicities well above 1 and the
	// comparison below is a genuine multiset comparison rather than a set
	// comparison that a de-duplicating bug would survive.
	p6DupPool = 3

	// p6FlushRounds bounds how many flush intervals one iteration may need to
	// drain. Two or three is the realistic number — one to flush the tail, plus
	// one for events the producer had not yet handed over — so this is slack, not
	// an expectation.
	p6FlushRounds = 32

	// p6RoundWait is how long each round waits for delivery before advancing the
	// clock again. Kept to a few poll steps so the common case of "the tail needs
	// exactly one tick" costs a few milliseconds, not a fixed timeout.
	p6RoundWait = 3 * waitPollStep
)

// p6Batching is one generated input: how many Log_Events to enqueue, the maximum
// Delivery_Batch size to enqueue them under, and whether messages repeat.
//
// Every field is exported so pbtFailf can render the input as JSON.
type p6Batching struct {
	// Events is the number of Log_Events enqueued, 0 through p6MaxEvents. Zero is
	// drawn on purpose: the empty sequence must deliver the empty multiset and
	// make zero requests (Req 5.12).
	Events int

	// BatchSize is the maximum Delivery_Batch size, 1 through p6MaxBatchSize
	// (Req 5.4).
	BatchSize int

	// Duplicates is true when messages are drawn from a pool of p6DupPool values
	// instead of being unique per event, so the enqueued multiset has
	// multiplicities greater than 1.
	//
	// Within-request order is not asserted in this mode, because a repeated
	// message cannot be mapped back to a single enqueue index. Uniqueness is what
	// makes the order check possible, multiplicity is what makes the multiset
	// check meaningful, and generating both modes gets both.
	Duplicates bool
}

// ExactMultiple reports whether every event leaves on the batch-size trigger
// with no tail (Req 5.5).
func (in p6Batching) ExactMultiple() bool {
	return in.Events > 0 && in.BatchSize > 0 && in.Events%in.BatchSize == 0
}

// Remainder reports how many events are left over after the last full batch.
// A non-zero remainder is the tail that only a flush tick releases (Req 5.7).
func (in p6Batching) Remainder() int {
	if in.BatchSize <= 0 {
		return 0
	}
	return in.Events % in.BatchSize
}

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

// p6Gen draws one input.
func p6Gen(r *rand.Rand) p6Batching {
	in := p6Batching{
		BatchSize:  p6GenBatchSize(r),
		Duplicates: pbtChance(r, 30),
	}
	in.Events = p6GenEvents(r, in.BatchSize)
	return in
}

// p6GenBatchSize draws a maximum Delivery_Batch size in 1..100, biased to the
// boundaries of the range.
//
// 1 and 2 are where an off-by-one in the trigger lives — at size 1 every event is
// its own batch, so a loop that appended before checking the length would send
// pairs — and 99 and 100 are where the batch never fills within the event budget,
// so the flush tick is the only thing that ever transmits.
func p6GenBatchSize(r *rand.Rand) int {
	if pbtChance(r, 55) {
		return pbtPick(r, []int{1, 1, 2, 2, 3, 99, 100, 100})
	}
	return 1 + r.Intn(p6MaxBatchSize)
}

// p6GenEvents draws an event count for the drawn batch size, aiming at both of
// the shapes the tail depends on:
//
//   - an **exact multiple** of the batch size, where every event leaves on the
//     batch-size trigger and no tail remains (Req 5.5), and
//   - a **remainder**, where the last events sit below the trigger and only a
//     flush tick releases them (Req 5.7).
//
// The distinction is the reason the runner advances the clock, so both shapes have
// to be generated or the loop that flushes the tail would never be exercised.
func p6GenEvents(r *rand.Rand, batchSize int) int {
	if pbtChance(r, 6) {
		return 0 // the empty sequence
	}
	if batchSize > p6MaxEvents {
		// No multiple of this batch size fits the budget, so every count here is
		// below the trigger: the whole sequence leaves on a flush tick.
		return 1 + r.Intn(p6MaxEvents)
	}

	multiplier := p6MaxEvents / batchSize // at least 1
	if pbtChance(r, 45) {
		return batchSize * (1 + r.Intn(multiplier)) // an exact multiple
	}
	if batchSize == 1 {
		// Every count is a multiple of 1; there is no remainder to draw.
		return 1 + r.Intn(p6MaxEvents)
	}
	// Some whole batches plus a remainder strictly between 0 and batchSize.
	return batchSize*r.Intn(multiplier) + 1 + r.Intn(batchSize-1)
}

// p6Messages returns the messages the iteration enqueues, in enqueue order.
//
// The unique mode returns exactly what deliveryEmit produces, so emission and
// expectation cannot drift apart. The duplicate mode draws the same messages from
// a pool of p6DupPool values.
func p6Messages(prefix string, in p6Batching) []string {
	if !in.Duplicates {
		return deliveryExpectedMessages(prefix, in.Events)
	}
	out := make([]string, in.Events)
	for i := range out {
		out[i] = deliveryMessage(prefix, i%p6DupPool)
	}
	return out
}

// ---------------------------------------------------------------------------
// Pure oracles
//
// The checks are pure functions over decoded bodies so that they are testable in
// their own right: TestP6ChecksDetectLossDuplicationAndReordering feeds each one
// a synthetic violation, which is what keeps a green property run from being
// evidence that the checks are unreachable.
// ---------------------------------------------------------------------------

// p6Multiset counts how many times each message appears, which is the multiset
// representation both sides of the comparison use.
func p6Multiset(messages []string) map[string]int {
	counts := make(map[string]int, len(messages))
	for _, message := range messages {
		counts[message]++
	}
	return counts
}

// p6ReceivedMultiset counts every message across every request body, ignoring
// how they were grouped — which is precisely the quantity the batch size must not
// change.
func p6ReceivedMultiset(batches [][]string) map[string]int {
	counts := make(map[string]int)
	for _, batch := range batches {
		for _, message := range batch {
			counts[message]++
		}
	}
	return counts
}

// p6MultisetViolation describes how the received multiset differs from the
// enqueued one, or returns the empty string when they are equal.
//
// Both directions are checked. Missing multiplicity is a lost or unflushed event;
// surplus multiplicity is a duplicated one; an unknown key is an event the test
// never enqueued at all.
func p6MultisetViolation(want, got map[string]int) string {
	var problems []string
	for message, n := range want {
		if got[message] != n {
			problems = append(problems,
				fmt.Sprintf("%q delivered %d time(s), want %d", message, got[message], n))
		}
	}
	for message, n := range got {
		if _, enqueued := want[message]; !enqueued {
			problems = append(problems,
				fmt.Sprintf("%q delivered %d time(s), want 0: it was never enqueued", message, n))
		}
	}
	if len(problems) == 0 {
		return ""
	}

	sort.Strings(problems) // map iteration order must not reorder a counterexample
	if len(problems) > 6 {
		problems = append(problems[:6], fmt.Sprintf("and %d further difference(s)", len(problems)-6))
	}
	return "the delivered multiset differs from the enqueued multiset: " + strings.Join(problems, "; ")
}

// p6BatchBoundViolation reports a request that carried more events than the
// configured maximum Delivery_Batch size (Req 5.5, 5.7), or the empty string.
//
// This is a bound, not a grouping claim: it says nothing about where the splits
// fell, only that no batch exceeded its size.
func p6BatchBoundViolation(batches [][]string, batchSize int) string {
	for i, batch := range batches {
		if len(batch) > batchSize {
			return fmt.Sprintf("request %d carried %d Log_Events, want at most the maximum Delivery_Batch size %d",
				i, len(batch), batchSize)
		}
	}
	return ""
}

// p6OrderViolation reports the first request whose events are not in enqueue
// order (Req 5.8), or the empty string.
//
// index maps a message to its enqueue position, so the check is that positions
// strictly increase *within* each request. Across requests nothing is claimed.
func p6OrderViolation(batches [][]string, index map[string]int) string {
	for request, batch := range batches {
		previous := -1
		for position, message := range batch {
			at, ok := index[message]
			if !ok {
				return fmt.Sprintf("request %d position %d carried %q, which was never enqueued",
					request, position, message)
			}
			if at <= previous {
				return fmt.Sprintf("request %d carried enqueue index %d at position %d, after index %d: within-request order is not enqueue order (%v)",
					request, at, position, previous, batch)
			}
			previous = at
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The property
// ---------------------------------------------------------------------------

// Feature: betterstack-error-logging, Property 6: Batching boundaries do not change the delivered multiset
// **Validates: Requirements 5.5, 5.7, 5.8, 7.1**
func TestProperty6_BatchingBoundariesDoNotChangeTheDeliveredMultiset(t *testing.T) {
	r, seed := pbtRand(t)

	batchSizes := make(map[int]bool)
	var (
		sawBatchSizeOne     bool
		sawBatchAboveBudget bool
		sawExactMultiple    bool
		sawRemainder        bool
		sawDuplicates       bool
		sawEmptySequence    bool
		sawMultipleRequests bool
	)

	for i := 0; i < pbtIterations; i++ {
		in := p6Gen(r)

		batchSizes[in.BatchSize] = true
		sawBatchSizeOne = sawBatchSizeOne || in.BatchSize == 1
		sawBatchAboveBudget = sawBatchAboveBudget || in.BatchSize > in.Events
		sawExactMultiple = sawExactMultiple || in.ExactMultiple()
		sawRemainder = sawRemainder || in.Remainder() > 0
		sawDuplicates = sawDuplicates || (in.Duplicates && in.Events > p6DupPool)
		sawEmptySequence = sawEmptySequence || in.Events == 0

		// A subtest per iteration so each iteration's httptest server and
		// delivery goroutine are torn down by its own t.Cleanup rather than
		// accumulating for the length of the run.
		iteration := i
		input := in
		t.Run(fmt.Sprintf("i%03d_events%d_batch%d", i, in.Events, in.BatchSize), func(t *testing.T) {
			if requests := p6Run(t, seed, iteration, input); requests > 1 {
				sawMultipleRequests = true
			}
		})
	}

	// Anti-vacuity. With no shrinking, a generator that collapsed onto one batch
	// size or never split a sequence across two requests would pass this property
	// while demonstrating nothing about confluence, and there would be no
	// counterexample to notice it from.
	if len(batchSizes) < 2 {
		t.Errorf("exercised %d distinct batch size(s) %v, want more than one: the property is about varying the batch size (seed %d)",
			len(batchSizes), p6SortedSizes(batchSizes), seed)
	}
	if !sawMultipleRequests {
		t.Errorf("no iteration produced more than one request (seed %d): every sequence fitted in a single Delivery_Batch, so no batch boundary was crossed",
			seed)
	}
	for _, c := range []struct {
		saw  bool
		what string
	}{
		{sawBatchSizeOne, "batch size 1, where every event is its own Delivery_Batch"},
		{sawBatchAboveBudget, "a batch size above the event count, where only a flush tick transmits (Req 5.7)"},
		{sawExactMultiple, "an event count that is an exact multiple of the batch size (Req 5.5)"},
		{sawRemainder, "an event count that leaves a remainder below the trigger (Req 5.7)"},
		{sawDuplicates, "repeated messages, so the enqueued multiset has multiplicities above 1"},
		{sawEmptySequence, "the empty sequence"},
	} {
		if !c.saw {
			t.Errorf("generator produced no iteration covering %s (seed %d)", c.what, seed)
		}
	}
}

// p6Run runs one iteration and returns how many requests the server received.
func p6Run(t *testing.T, seed int64, iteration int, in p6Batching) int {
	t.Helper()

	// Req 7.1: every batch is accepted, so no outcome other than delivery is
	// reachable and any missing event is a batching defect rather than a drop.
	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()

	// The queue is sized above the event count so nothing can be dropped for want
	// of capacity. A capacity drop is Req 6.3's subject and P7's property; here it
	// would only make the delivered multiset legitimately short and mask the claim
	// under test.
	s := startDeliveryShipper(t, deliveryConfig(server.URL, in.BatchSize, in.Events+8, clock), clock)

	if got := s.batchSize(); got != in.BatchSize {
		pbtFatalf(t, seed, iteration, in, "the Shipper is batching at %d, want the generated size %d", got, in.BatchSize)
	}

	prefix := fmt.Sprintf("p6-%d", iteration)
	messages := p6Messages(prefix, in)
	logger := deliveryLogger(s)
	if in.Duplicates {
		for _, message := range messages {
			logger.Error(message)
		}
	} else {
		deliveryEmit(logger, prefix, in.Events)
	}

	// Advance the fake clock until everything has reached a terminal state. This
	// is the step the property cannot do without: any event count that is not an
	// exact multiple of the batch size leaves a tail below the trigger, and that
	// tail is released by the flush interval elapsing, not by the count (Req 5.7).
	if !p6WaitDelivered(s, clock, in.Events) {
		snap := s.Snapshot()
		pbtFatalf(t, seed, iteration, in,
			"Delivered = %d after %d flush intervals, want %d (%+v; %d request(s) received)",
			snap.Delivered, p6FlushRounds, in.Events, snap, server.requests())
	}

	batches := server.batches(t)

	// The property itself: same events, same multiplicity, whatever the batch size
	// did to the grouping (Req 5.5, 5.7, 7.1).
	if violation := p6MultisetViolation(p6Multiset(messages), p6ReceivedMultiset(batches)); violation != "" {
		pbtFailf(t, seed, iteration, in, "%s (%d request(s), grouping %v)", violation, len(batches), p6Sizes(batches))
	}

	// No batch may exceed the configured maximum, however the events arrived.
	if violation := p6BatchBoundViolation(batches, in.BatchSize); violation != "" {
		pbtFailf(t, seed, iteration, in, "%s (grouping %v)", violation, p6Sizes(batches))
	}

	// Req 5.8: within one request, enqueue order. Only checkable when messages
	// identify their enqueue position, which the duplicate mode gives up.
	if !in.Duplicates {
		index := make(map[string]int, len(messages))
		for i, message := range messages {
			index[message] = i
		}
		if violation := p6OrderViolation(batches, index); violation != "" {
			pbtFailf(t, seed, iteration, in, "%s", violation)
		}
	}

	// Nothing was dropped, nothing was left behind, and the accounting balances:
	// a multiset that matched while the counters disagreed would mean the events
	// arrived by some path this property is not describing.
	snap := s.Snapshot()
	switch {
	case snap.Enqueued != uint64(in.Events):
		pbtFailf(t, seed, iteration, in, "Enqueued = %d, want %d admissions (%+v)", snap.Enqueued, in.Events, snap)
	case snap.Delivered != uint64(in.Events):
		pbtFailf(t, seed, iteration, in, "Delivered = %d, want %d (%+v)", snap.Delivered, in.Events, snap)
	case snap.Dropped != 0:
		pbtFailf(t, seed, iteration, in, "Dropped = %d against a server that always returns 202, want 0 (%+v)", snap.Dropped, snap)
	case snap.InFlight != 0 || snap.QueueDepth != 0:
		pbtFailf(t, seed, iteration, in, "InFlight = %d and QueueDepth = %d after every event was delivered, want 0 and 0 (%+v)",
			snap.InFlight, snap.QueueDepth, snap)
	}
	assertCounterIdentities(t, "after every generated batch was accepted", snap)

	return len(batches)
}

// p6WaitDelivered advances the flush interval until want events have been
// delivered, and reports whether they were.
//
// Advancing in a loop rather than once is what makes the tail deterministic
// without pinning the interleaving: at the moment the producer returns, some
// events may still be in the Event_Queue rather than in `pending`, so a single
// tick can flush only what the delivery goroutine had already taken. Each round
// therefore waits briefly for progress and then releases another interval, which
// converges in two or three rounds and is bounded at p6FlushRounds.
//
// Every wait is a poll on the counters (Req 12.2): no round sleeps for the flush
// interval, it advances the fake clock past it.
func p6WaitDelivered(s *Shipper, clock *fakeClock, want int) bool {
	delivered := func() bool { return s.Snapshot().Delivered >= uint64(want) }

	for round := 0; round < p6FlushRounds; round++ {
		if waitFor(delivered, p6RoundWait) {
			return true
		}
		clock.Advance(defaultFlushSeconds * time.Second) // Req 5.7
	}
	// One generous last look, so a slow machine reports a real failure rather
	// than a scheduling artefact.
	return waitFor(delivered, deliveryWaitTimeout)
}

// p6Sizes summarises a grouping as the event count of each request, which is the
// one thing a counterexample needs about grouping even though nothing is asserted
// about it.
func p6Sizes(batches [][]string) []int {
	sizes := make([]int, len(batches))
	for i, batch := range batches {
		sizes[i] = len(batch)
	}
	return sizes
}

// p6SortedSizes renders the set of exercised batch sizes deterministically.
func p6SortedSizes(sizes map[int]bool) []int {
	out := make([]int, 0, len(sizes))
	for size := range sizes {
		out = append(out, size)
	}
	sort.Ints(out)
	return out
}

// ---------------------------------------------------------------------------
// Oracle self-tests
// ---------------------------------------------------------------------------

// TestP6ChecksDetectLossDuplicationAndReordering proves each check fires on the
// defect it exists for, so a green Property 6 run is evidence about batching
// rather than evidence that the comparison is unreachable.
func TestP6ChecksDetectLossDuplicationAndReordering(t *testing.T) {
	enqueued := []string{"a", "b", "c", "b"} // "b" twice: a genuine multiset
	want := p6Multiset(enqueued)
	index := map[string]int{"a": 0, "b": 1, "c": 2}

	t.Run("regrouping is not a violation", func(t *testing.T) {
		// The same four events under three different groupings, which is exactly
		// what varying the batch size produces.
		for _, batches := range [][][]string{
			{{"a", "b", "c", "b"}},
			{{"a"}, {"b"}, {"c"}, {"b"}},
			{{"a", "b"}, {"c", "b"}},
		} {
			if got := p6MultisetViolation(want, p6ReceivedMultiset(batches)); got != "" {
				t.Errorf("p6MultisetViolation on regrouped delivery %v = %q, want no violation", batches, got)
			}
		}
	})

	t.Run("a lost event is a violation", func(t *testing.T) {
		got := p6ReceivedMultiset([][]string{{"a", "b"}, {"c"}}) // the tail "b" never flushed
		if p6MultisetViolation(want, got) == "" {
			t.Error("p6MultisetViolation with an unflushed tail = no violation, want a violation")
		}
	})

	t.Run("a duplicated event is a violation", func(t *testing.T) {
		got := p6ReceivedMultiset([][]string{{"a", "b", "c", "b"}, {"c"}}) // "c" resent
		if p6MultisetViolation(want, got) == "" {
			t.Error("p6MultisetViolation with a duplicated event = no violation, want a violation")
		}
	})

	t.Run("an event that was never enqueued is a violation", func(t *testing.T) {
		got := p6ReceivedMultiset([][]string{{"a", "b", "c", "b", "z"}})
		if p6MultisetViolation(want, got) == "" {
			t.Error("p6MultisetViolation with an unknown event = no violation, want a violation")
		}
	})

	t.Run("an oversized batch is a violation", func(t *testing.T) {
		batches := [][]string{{"a", "b"}, {"c"}}
		if got := p6BatchBoundViolation(batches, 2); got != "" {
			t.Errorf("p6BatchBoundViolation at the bound = %q, want no violation", got)
		}
		if p6BatchBoundViolation(batches, 1) == "" {
			t.Error("p6BatchBoundViolation on a batch of 2 against size 1 = no violation, want a violation")
		}
	})

	t.Run("within-request reordering is a violation", func(t *testing.T) {
		if got := p6OrderViolation([][]string{{"a", "b"}, {"c"}}, index); got != "" {
			t.Errorf("p6OrderViolation on in-order requests = %q, want no violation", got)
		}
		// Across requests nothing is claimed: a later index in an earlier request
		// is grouping, not reordering.
		if got := p6OrderViolation([][]string{{"c"}, {"a", "b"}}, index); got != "" {
			t.Errorf("p6OrderViolation across requests = %q, want no violation: grouping is not asserted", got)
		}
		if p6OrderViolation([][]string{{"b", "a"}}, index) == "" {
			t.Error("p6OrderViolation on a reordered request = no violation, want a violation (Req 5.8)")
		}
	})
}

// TestP6GeneratorCoversBothTailShapes pins the generator's contract: every drawn
// input is runnable and inside the work budget, and both tail shapes — exact
// multiple and remainder — are actually drawn.
//
// It is a cheap guard against a later edit to the drawing weights quietly turning
// the property into a single-shape test, which is the failure mode a
// no-shrinking harness cannot report for itself.
func TestP6GeneratorCoversBothTailShapes(t *testing.T) {
	r, seed := pbtRand(t)

	var exact, remainder, empty int
	for i := 0; i < pbtIterations; i++ {
		in := p6Gen(r)
		switch {
		case in.BatchSize < 1 || in.BatchSize > p6MaxBatchSize:
			pbtFailf(t, seed, i, in, "BatchSize = %d, want 1..%d", in.BatchSize, p6MaxBatchSize)
		case in.Events < 0 || in.Events > p6MaxEvents:
			pbtFailf(t, seed, i, in, "Events = %d, want 0..%d", in.Events, p6MaxEvents)
		}

		switch {
		case in.Events == 0:
			empty++
		case in.ExactMultiple():
			exact++
		default:
			remainder++
		}
	}

	if exact == 0 || remainder == 0 {
		t.Errorf("drew %d exact-multiple and %d remainder input(s) (%d empty), want at least one of each (seed %d)",
			exact, remainder, empty, seed)
	}
}
