package logging

// Property 9 lives here, alone, so that the property this file asserts is
// obvious from its name and so that concurrently developed property tests cannot
// collide in one file. Every identifier declared here is prefixed p9 for the same
// reason.
//
// What this file is about, and why it is not simply "enqueue is fast":
// TestEnqueueIsWaitFreeAtCapacity in shipper_test.go already times a burst
// against a Shipper that has no server at all, which shows the select's default
// arm costs tens of nanoseconds. It cannot show the thing Req 5.13 actually
// promises — that the cost is *independent of the ingest endpoint* — because
// nothing in that test is ever waiting on the network.
//
// So the setup here puts a real HTTP request in flight and leaves it there. The
// test server's handler blocks on a channel the test never closes until cleanup,
// so a genuine POST is stalled, mid-response, on a loopback connection for the
// entire duration of the property loop. The stall has no bound: it outlives every
// assertion below. That is what makes the linear bound a statement about
// independence rather than about speed — a Shipper whose enqueue path touched the
// stalled request in any way (a lock held across the Send, a blocking channel
// send to a busy delivery goroutine, an unbuffered handoff) would not be *slow*
// here, it would never finish, and the completion deadline would catch it.
//
// The design's P9 row asks for the timing bound "with generous slack to stay
// non-flaky". Timing assertions are the flakiest thing a CI box can run, so each
// burst is checked twice, in increasing order of trustworthiness:
//
//  1. **Completion.** The burst runs on its own goroutine and the test waits on a
//     select with a deadline. This is a "did every enqueue return at all" check,
//     it is immune to load, and it is the assertion that actually distinguishes
//     wait-free from blocking.
//  2. **A linear bound.** p9FixedSlack + N x p9PerOpBudget, with the per-operation
//     budget set an order of magnitude *under* Req 5.2's 1 ms and three orders of
//     magnitude *over* the observed cost. It catches a per-call regression into
//     the microsecond range while leaving room for scheduler noise, GC, and the
//     -race instrumentation.
//
// How the stall is created. The first test does not start the Shipper at all — the
// per-operation outcome has to be a deterministic function of (N, capacity) — so
// its stall is created by driving IngestClient.Send on a background goroutine:
// the same client the Shipper owns, against the same endpoint, blocked inside the
// same http.Client the delivery goroutine uses. The second test starts the
// Shipper, so from task 6.5 onward the delivery goroutine parks inside its own
// Send as well, and both stalls are held open for the whole burst.
//
// Req 12.2 and 12.3 are respected throughout: the endpoint is loopback, no real
// sleep exceeds 50 ms (waitFor polls in 1 ms steps and the deadlines below are
// only ever *reached* on failure), and the whole file is bounded by the work of
// roughly 30,000 enqueues, which is microseconds of CPU.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// p9FixedSlack absorbs the one-off costs that are not per-operation: the
	// goroutine start, the first timer read, and whatever the scheduler was doing
	// when the burst began.
	p9FixedSlack = 5 * time.Millisecond

	// p9PerOpBudget is the linear term of the oracle. Req 5.2 and 6.3 allow 1 ms
	// per enqueue; a measured enqueue costs 50-100 ns, and a few hundred ns under
	// -race. 100 us therefore sits ~1000x above the real cost and 10x below the
	// requirement: loose enough that no loaded CI box trips it, tight enough that
	// a regression into a syscall, a lock, or a blocking handoff cannot hide.
	p9PerOpBudget = 100 * time.Microsecond

	// p9CompletionDeadline bounds the load-independent assertion: every enqueue in
	// the burst must have returned by now. It is only ever *waited out* when the
	// property is violated, so its size costs nothing in the passing case, and it
	// is far larger than any plausible bound so that reaching it means "blocked",
	// not "slow".
	p9CompletionDeadline = 2 * time.Second

	// p9StallDeadline bounds how long the test waits for the stalled request to
	// arrive at the server. Reaching it means the setup failed, not that the
	// property did.
	p9StallDeadline = time.Second
)

// Feature: betterstack-error-logging, Property 9: Enqueue never blocks on the network
// **Validates: Requirements 5.2, 5.13**
//
// For any burst of N enqueue operations at any Event_Queue capacity, performed
// while a real POST to the ingest endpoint is stalled with no response in sight:
//
//   - every one of the N operations returns (none is left parked on the network);
//   - the total elapsed time stays under p9FixedSlack + N x p9PerOpBudget, a bound
//     that is linear in N and contains no term for the server's response time
//     (Req 5.2, 5.13);
//   - the outcome of each operation is exactly what the capacity dictates: the
//     first min(N, capacity) events occupy the queue and the remaining
//     max(0, N-capacity) are counted as reasonQueueFull drops (Req 6.3).
//
// The generator covers both arms of the select deliberately: bursts strictly
// below the capacity never reach the default arm, and bursts far past it reach
// almost nothing else. Both are timed, because "never blocks" has to hold for the
// successful send as much as for the refusal.
//
// The Shipper is deliberately *not* started here, so nothing drains the queue and
// the per-operation outcome is a deterministic function of (N, capacity). The
// started-Shipper case is the second test below.
func TestProperty9_EnqueueNeverBlocksOnAStalledIngestEndpoint(t *testing.T) {
	r, seed := pbtRand(t)

	server := newP9StalledServer(t)
	// The client is constructed exactly as NewShipper constructs the Shipper's
	// own: same URL, same token, same 10 s per-request timeout. The stalled
	// request below therefore occupies the same kind of connection, in the same
	// kind of http.Client, that the delivery goroutine will occupy once task 6.5
	// lands.
	p9StallOneDelivery(t, NewIngestClient(server.URL, testShipperToken, DefaultRequestTimeout), server)

	ev := testEvent(t, "p9 burst event")

	// Non-vacuity counters. Both arms of the select must actually be exercised: a
	// generator drift that only ever produced over-capacity bursts would leave
	// the successful-send half of Req 5.13 unchecked, and vice versa. The floors
	// are far below the observed split (roughly 45/55), so they fail on a real
	// loss of coverage rather than on an unlucky seed.
	sawQueueFull, sawFreeCapacity := 0, 0

	for iteration := 0; iteration < pbtIterations; iteration++ {
		burst := p9GenBurst(r)

		s := NewShipper(p9Config(server, burst.Capacity))

		elapsed, completed := p9RunBurst(burst.Operations, func() { s.enqueue(ev) })

		// Clause 1, the load-independent one: nothing was left blocked.
		if !completed {
			pbtFatalf(t, seed, iteration, burst,
				"a burst of %d enqueues at capacity %d had not finished %s after it began, while one POST to %s was stalled with no response: at least one enqueue is blocked on the network (Req 5.2, 5.13)",
				burst.Operations, burst.Capacity, p9CompletionDeadline, server.URL)
		}

		// Clause 2: the linear bound. The stall outlives this measurement by the
		// whole remainder of the test, so any dependence of enqueue on the
		// endpoint's response time would be unbounded, not merely large.
		if bound := p9Bound(burst.Operations); elapsed > bound {
			pbtFailf(t, seed, iteration, burst,
				"%d enqueues at capacity %d took %s, want at most %s (%s fixed + %d x %s per operation); Req 5.2 allows 1ms per enqueue and Req 5.13 requires that figure to be independent of the ingest endpoint, which is stalled here",
				burst.Operations, burst.Capacity, elapsed, bound, p9FixedSlack, burst.Operations, p9PerOpBudget)
		}

		// Clause 3: every operation reached the outcome its capacity dictates.
		// This is what stops the property passing vacuously on an enqueue that
		// returned quickly by doing nothing at all.
		wantQueued := burst.Operations
		if wantQueued > burst.Capacity {
			wantQueued = burst.Capacity
		}
		wantDropped := burst.Operations - wantQueued

		if got := len(s.queue); got != wantQueued {
			pbtFailf(t, seed, iteration, burst,
				"len(queue) = %d after %d enqueues at capacity %d, want %d (Req 6.2)",
				got, burst.Operations, burst.Capacity, wantQueued)
		}
		snap := s.Snapshot()
		if got := snap.DroppedByReason[reasonQueueFull]; got != uint64(wantDropped) {
			pbtFailf(t, seed, iteration, burst,
				"DroppedByReason[reasonQueueFull] = %d after %d enqueues at capacity %d, want %d (Req 6.3)",
				got, burst.Operations, burst.Capacity, wantDropped)
		}
		if snap.Dropped != uint64(wantDropped) {
			pbtFailf(t, seed, iteration, burst,
				"Dropped = %d after %d enqueues at capacity %d, want %d: a stalled endpoint must not produce a drop for any other reason",
				snap.Dropped, burst.Operations, burst.Capacity, wantDropped)
		}

		if wantDropped > 0 {
			sawQueueFull++
		}
		if wantQueued == burst.Operations {
			sawFreeCapacity++
		}
	}

	// The stalled request must still be stalled: if the server had answered, every
	// measurement above would have been taken against a *responsive* endpoint and
	// the property would be the weaker "enqueue is fast" claim.
	if got := server.inFlight(); got != 1 {
		t.Errorf("%d requests were in flight at the end of the property loop, want exactly 1 still stalled: the measurements above were not taken against an unresponsive endpoint",
			got)
	}

	const minEachArm = 10
	if sawQueueFull < minEachArm {
		t.Errorf("only %d of %d bursts exceeded the queue capacity, want at least %d: the queue-full arm of the select is close to unexercised",
			sawQueueFull, pbtIterations, minEachArm)
	}
	if sawFreeCapacity < minEachArm {
		t.Errorf("only %d of %d bursts fitted inside the queue capacity, want at least %d: the successful-send arm of the select is close to unexercised",
			sawFreeCapacity, pbtIterations, minEachArm)
	}
}

// Feature: betterstack-error-logging, Property 9: Enqueue never blocks on the network
// **Validates: Requirements 5.2, 5.13**
//
// The same property on the full production path: records emitted through a
// slog.Logger bound to NewFanoutHandler(console, shipper.Handler()), with the
// delivery goroutine running and consuming the queue, while a POST issued through
// *that Shipper's own* IngestClient is stalled with no response.
//
// This is the arrangement Req 5.13 describes literally — "WHILE the Ingest_Client
// waits for a response ... the Better_Stack_Handler SHALL continue accepting
// Log_Events" — and it adds two things the direct-enqueue loop above cannot see:
// the delivery goroutine is a live competing consumer of the queue, and the
// admission counter is incremented by Handle, so the Req 11.4 and 11.5 accounting
// identities are checkable.
//
// Exact counts are deliberately not asserted: with a real consumer draining
// concurrently, how many of the burst land in the queue versus in flight is a
// scheduling outcome, and pinning it would be asserting a race. What is asserted
// is what must hold regardless of the interleaving.
//
// Since task 6.5 the delivery goroutine transmits, so two requests are stalled
// while this burst runs: the one the test issued, and run's own first
// Delivery_Batch. The count of requests is asserted at the end, because "exactly
// one batch from run, however many records arrive" is Req 5.15.
func TestProperty9_HandlePathKeepsAcceptingWhileADeliveryAttemptIsStalled(t *testing.T) {
	const (
		capacity = 64
		records  = 500
	)

	server := newP9StalledServer(t)
	s := NewShipper(p9Config(server, capacity))

	// The Shipper's own client, so the stalled request occupies the very
	// http.Client the delivery goroutine transmits through.
	p9StallOneDelivery(t, s.client, server)

	s.Start()
	t.Cleanup(func() { close(s.closeDone) })

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler()))

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		emitErrors(logger, records, "p9 handle path record")
		done <- time.Since(start)
	}()

	var elapsed time.Duration
	select {
	case elapsed = <-done:
	case <-time.After(p9CompletionDeadline):
		t.Fatalf("%d ERROR records had not finished being emitted %s after they began, while one POST to %s was stalled: the Handle path is blocked on the network (Req 5.2, 5.13)",
			records, p9CompletionDeadline, server.URL)
	}

	// A far looser bound than p9Bound: this path also converts each Log_Record,
	// redacts it, and writes a JSON line to the console buffer, none of which is
	// the mechanism under test. It is still linear in the record count and still
	// contains no term for the server.
	if bound := p9FixedSlack + records*10*p9PerOpBudget; elapsed > bound {
		t.Errorf("%d records through the Handle path took %s, want at most %s while the endpoint is stalled (Req 5.13)",
			records, elapsed, bound)
	}

	if got := consoleLines(console); got != records {
		t.Errorf("console wrote %d records, want %d: a stalled endpoint must not cost a single stdout line (Req 1.10)",
			got, records)
	}

	snap := s.Snapshot()
	if snap.Enqueued != records {
		t.Errorf("Enqueued = %d, want %d admissions", snap.Enqueued, records)
	}
	assertCounterIdentities(t, "after a burst through Handle while a delivery attempt is stalled", snap)

	// Two requests are stalled now, not one: the test's own, and the delivery
	// goroutine's first Delivery_Batch. That second stall is the strengthening
	// this fixture was built for, and the *count* is a direct observation of
	// Req 5.15 — 500 records arrived and the batch-size trigger was reached
	// twenty-five times over, yet the delivery goroutine issued exactly one
	// request, because Send is synchronous inside its loop and no second
	// transmission can begin while one is in progress.
	if !waitFor(func() bool { return server.served() >= 2 }, p9CompletionDeadline) {
		t.Errorf("the server received %d requests, want the test's stalled request plus the delivery goroutine's first Delivery_Batch",
			server.served())
	}
	// Give a second delivery request the chance to arrive, so the equality below
	// is evidence rather than a race won.
	time.Sleep(20 * waitPollStep)
	if got := server.served(); got != 2 {
		t.Errorf("the server received %d requests, want exactly 2 — the test's stall plus one Delivery_Batch: no further request may be issued while one is in progress (Req 5.15)",
			got)
	}
	if got := server.inFlight(); got != 2 {
		t.Errorf("%d requests were in flight at the end of the burst, want exactly 2 still stalled", got)
	}
}

// ---------------------------------------------------------------------------
// The stalled endpoint
// ---------------------------------------------------------------------------

// p9StalledServer is an httptest.Server that accepts a request and then never
// answers it: the handler parks on a channel nothing closes until cleanup.
//
// That is the whole point of the fixture. A server that responded slowly would
// make the property a claim about a *duration*, and any duration a test can
// afford to wait is a duration a blocking enqueue could survive. A server that
// never responds makes "enqueue waits for the endpoint" mean "enqueue never
// returns", which a deadline detects with no timing sensitivity at all.
//
// The handler also selects on the request context, so a client-side timeout or
// cancellation releases the handler goroutine rather than pinning it until
// cleanup.
type p9StalledServer struct {
	*httptest.Server

	// release is closed exactly once, by releaseAll, during cleanup.
	release     chan struct{}
	releaseOnce sync.Once

	requests atomic.Int64 // requests that have arrived
	parked   atomic.Int64 // requests currently parked in the handler
}

// newP9StalledServer starts the stalled server and registers its shutdown.
//
// Cleanup order matters: httptest.Server.Close waits for outstanding handlers to
// return, so release must be closed first or Close would block until the test
// binary's own timeout. That is why the shutdown closure calls releaseAll before
// Close, and why releaseAll is idempotent — the caller that waits for the stalled
// Send to return calls it too.
func newP9StalledServer(t *testing.T) *p9StalledServer {
	t.Helper()

	server := &p9StalledServer{release: make(chan struct{})}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body so the stall is on the *response*, with the
		// batch already fully transmitted. A request still being written is a
		// weaker stall: it could be mistaken for backpressure on the send side.
		_, _ = io.Copy(io.Discard, r.Body)

		server.requests.Add(1)
		server.parked.Add(1)
		defer server.parked.Add(-1)

		select {
		case <-server.release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		server.releaseAll()
		server.Close()
	})

	// Req 12.3: no test may reach the network.
	assertLoopbackEndpoint(t, server.URL)
	return server
}

// releaseAll unblocks every parked handler. Safe to call any number of times.
func (s *p9StalledServer) releaseAll() {
	s.releaseOnce.Do(func() { close(s.release) })
}

// inFlight reports how many requests are currently parked in the handler.
func (s *p9StalledServer) inFlight() int { return int(s.parked.Load()) }

// served reports how many requests have arrived over the server's lifetime.
func (s *p9StalledServer) served() int { return int(s.requests.Load()) }

// p9StallOneDelivery issues one real Delivery_Batch POST through client on a
// background goroutine and returns once the server has the request parked, so
// that every assertion made afterwards is made while a transmission is genuinely
// outstanding.
//
// It returns after confirming the stall rather than after merely launching the
// goroutine: "a request is in flight" has to be observed, or the property could
// be measured against an endpoint that had not been contacted yet.
//
// The registered cleanup releases the server and then waits for Send to return,
// so the goroutine cannot outlive the test and land in a later test's goroutine
// baseline (stableGoroutineCount tolerates goroutines that are exiting, not ones
// that never exit).
func p9StallOneDelivery(t *testing.T, client *IngestClient, server *p9StalledServer) {
	t.Helper()

	before := server.served()

	done := make(chan SendOutcome, 1)
	go func() {
		done <- client.Send(context.Background(), []*Event{{Message: "p9 stalled batch"}}, 0)
	}()

	if !waitFor(func() bool { return server.inFlight() > 0 && server.served() > before }, p9StallDeadline) {
		// Nothing below this point would mean anything without the stall, so this
		// is fatal rather than an error.
		t.Fatalf("no request reached the stalled server at %s within %s (served %d, in flight %d): the property cannot be tested without an outstanding transmission",
			server.URL, p9StallDeadline, server.served(), server.inFlight())
	}

	t.Cleanup(func() {
		server.releaseAll()
		select {
		case <-done:
		case <-time.After(p9CompletionDeadline):
			t.Errorf("the stalled delivery attempt did not return within %s of the server being released", p9CompletionDeadline)
		}
	})
}

// p9Config returns an enabled Config addressed at the stalled server.
//
// It builds the Config directly instead of going through ResolveConfig, which
// newTestShipperConfig uses, for one reason: ResolveConfig requires an https
// scheme (Req 1.6) and httptest.NewServer serves http. Req 12.1 is explicit that
// a Config supplied at construction is how tests direct traffic at an
// httptest.Server, so injecting the loopback URL here is the sanctioned path
// rather than a workaround. Every other field carries the same value ResolveConfig
// would have produced.
//
// The clock is the fake one (Req 12.2), so a Shipper built from this Config reads
// no wall clock for flush or backoff timing. The one thing this file *does* measure
// in wall-clock terms is the caller-visible duration of enqueue, which is the
// quantity Req 5.2 and 5.13 are stated in and cannot be faked.
func p9Config(server *p9StalledServer, queueSize int) Config {
	return Config{
		Enabled:     true,
		IngestURL:   server.URL,
		SourceToken: testShipperToken,
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   defaultBatchSize,
		FlushEvery:  defaultFlushSeconds * time.Second,
		QueueSize:   queueSize,
		DiagWriter:  io.Discard,
		Clock:       newFakeClock(),
	}
}

// ---------------------------------------------------------------------------
// The burst: generation, execution, oracle
// ---------------------------------------------------------------------------

// p9Burst is one generated input: how much queue capacity the Shipper has, and
// how many enqueue operations are performed against it.
//
// Exported fields, because pbtFailf reports a counterexample as JSON.
type p9Burst struct {
	Capacity   int
	Operations int
}

// p9CapacityBoundaries are the capacities where an off-by-one in the select would
// show: 1 is "every enqueue after the first is refused", and 2 is the smallest
// capacity where a queue can be partially full.
var p9CapacityBoundaries = []int{1, 2, 8, 64}

// p9GenCapacity returns an Event_Queue capacity biased toward those boundaries.
// It is always at least 1, because NewShipper clamps a non-positive capacity to
// the 1000-event default and that would silently change the oracle.
func p9GenCapacity(r *rand.Rand) int {
	if pbtChance(r, 60) {
		return pbtPick(r, p9CapacityBoundaries)
	}
	return 1 + r.Intn(64)
}

// p9GenBurst returns one generated burst, sampling three regimes on purpose:
// exactly around the capacity, well past it, and a uniform draw either side.
//
// The regimes exist because the two arms of the select have different costs. A
// successful buffered send touches the channel's ring buffer; the default arm
// touches a counter. Req 5.13 covers both, so both are timed, and the transition
// between them — capacity-1, capacity, capacity+1 — is sampled explicitly rather
// than left to a uniform draw that would rarely land on it.
func p9GenBurst(r *rand.Rand) p9Burst {
	capacity := p9GenCapacity(r)

	var operations int
	switch {
	case pbtChance(r, 40):
		// The boundary itself: one under the capacity, exactly at it, one over.
		operations = capacity + pbtPick(r, []int{-1, 0, 1})
		if operations < 1 {
			operations = 1
		}
	case pbtChance(r, 40):
		// Well past the capacity, so nearly every operation takes the default
		// arm and the measurement is dominated by refusals.
		operations = capacity + 1 + r.Intn(500)
	default:
		operations = 1 + r.Intn(2*capacity+2)
	}

	return p9Burst{Capacity: capacity, Operations: operations}
}

// p9Bound is the oracle: the largest total duration a burst of the given size may
// take. It is linear in the operation count and contains no term describing the
// ingest endpoint, which is the formal content of Req 5.13.
func p9Bound(operations int) time.Duration {
	return p9FixedSlack + time.Duration(operations)*p9PerOpBudget
}

// p9RunBurst performs op the given number of times on a dedicated goroutine and
// reports how long that took, or completed == false when the burst had not
// finished within p9CompletionDeadline.
//
// The goroutine is what makes the blocking case *detectable* instead of merely
// slow: a blocking enqueue against a server that never responds would hang the
// test goroutine forever and produce a package timeout with a stack trace rather
// than a counterexample naming the burst. Running it off to the side turns that
// into an ordinary assertion.
//
// On the failure path the goroutine is knowingly abandoned — it is blocked, so
// there is nothing to wait for — and the caller reports the violation fatally, so
// no later test inherits the confusion.
func p9RunBurst(operations int, op func()) (elapsed time.Duration, completed bool) {
	return p9RunBurstWithin(operations, op, p9CompletionDeadline)
}

// p9RunBurstWithin is p9RunBurst with an explicit deadline, which exists so the
// detector itself can be self-tested against an operation that never returns
// without the self-test waiting out the full p9CompletionDeadline.
func p9RunBurstWithin(operations int, op func(), deadline time.Duration) (elapsed time.Duration, completed bool) {
	// Buffered, so the burst goroutine can always publish its result and exit,
	// even after this function has already given up waiting for it.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		for i := 0; i < operations; i++ {
			op()
		}
		done <- time.Since(start)
	}()

	select {
	case elapsed = <-done:
		return elapsed, true
	case <-time.After(deadline):
		return 0, false
	}
}

// TestP9RunBurstDetectsAnOperationThatNeverReturns is a self-test of the
// detector, not of the Shipper.
//
// Every clause of Property 9 rests on p9RunBurst reporting completed == false for
// an operation that parks forever. If it reported true — a select arm written the
// wrong way round, a deadline that is never armed — both property tests above
// would pass against a Shipper whose enqueue blocked on the network, which is the
// exact bug they exist to catch. So the failing direction is exercised here
// deliberately, with an operation that does the one thing enqueue's default arm
// exists to prevent: wait on something that never becomes ready.
//
// The deadline is 20 ms rather than p9CompletionDeadline, which keeps this test
// inside the Req 12.2 ceiling on real sleeps.
func TestP9RunBurstDetectsAnOperationThatNeverReturns(t *testing.T) {
	const selfTestDeadline = 20 * time.Millisecond

	// Closed at the end, so the parked goroutine exits and no goroutine leaks
	// into a later test's baseline.
	never := make(chan struct{})
	defer close(never)

	if _, completed := p9RunBurstWithin(1, func() { <-never }, selfTestDeadline); completed {
		t.Fatalf("p9RunBurstWithin reported completion for an operation that blocks forever: the blocking case is undetectable, so Property 9 cannot fail")
	}

	// The passing direction, so the detector is not merely reporting false for
	// everything.
	if _, completed := p9RunBurstWithin(1000, func() {}, selfTestDeadline); !completed {
		t.Errorf("p9RunBurstWithin reported non-completion for 1000 no-op operations within %s", selfTestDeadline)
	}
}
