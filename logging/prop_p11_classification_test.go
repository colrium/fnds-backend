package logging

// Property 11: Status classification determines attempt count.
//
// *For any* HTTP status code returned by the ingest endpoint, the
// transmission-attempt count equals 1 for 202, for every other status in
// 200-299, for 402, 403, 406, and 413, and for every status outside 200-299 that
// is neither 429 nor within 500-599; and lies in 2-5 for 429 and for statuses in
// 500-599.
//
// This is the **model-based** property of the design's P11 row: "fake clock,
// server returns a generated status; count requests. Oracle: classification
// table from Req 7 as a Go map." It is therefore in two halves, and the split is
// forced by what an httptest.Server can physically do:
//
//  1. **The pure classifier over arbitrary statuses.** classify is fed every
//     status the harness generates — including 199 and 600, which net/http will
//     not deliver to a client as a final status, and negative and enormous
//     values that only a hand-built SendOutcome can carry — and its verdict is
//     compared against p11Oracle. Req 7.15 is written over the whole complement
//     of the retryable and delivered sets, so a classifier with a hole in it
//     would fail *open* into a retry loop; this half is the only way to see that.
//  2. **End-to-end attempt counting**, for the statuses a server can actually
//     return (200-599). A real Shipper drives a real client at an endpoint that
//     always answers the generated status, the fake clock is advanced through the
//     backoff schedule, and the requests the endpoint received are counted. That
//     is what turns "classify says retry" into the claim the requirement
//     actually makes, which is about HTTP requests (Req 7.6).
//
// **Why the oracle is a map plus ranges rather than a second switch.** The point
// of a model-based property is that the model and the implementation can fail
// independently. p11Oracle is derived from the *text* of Req 7 — the individually
// named statuses as map keys, the two ranges as comparisons, everything else as
// the default of Req 7.15 — and deliberately does not mirror the order-sensitive
// switch in classify.go. A transposition inside that switch (429 matched after the
// 5xx range, say, or 402 after the 2xx range) changes the implementation's answer
// and not the oracle's.
//
// **Why the fake clock must be advanced, and what happens if it is not.** A
// retryable status makes the delivery goroutine park in
// waitBeforeRetransmit -> retryTimer -> clock.After, which on the injected
// fakeClock never fires by itself. Without an explicit Advance the goroutine sits
// in the first backoff forever, the endpoint receives exactly one request, and
// the property would "pass" for 429 and every 5xx while asserting nothing about
// retry at all. So each iteration walks the schedule the way
// TestSD5xxRetriedToExhaustionAbandonsTheBatch does: wait for backoff i to be
// registered on the clock, then advance by exactly the duration that was
// registered. The duration itself is P16's subject (task 8.5) and is read here
// rather than asserted, so that a schedule change cannot fail this property.
//
// Every identifier here is `p11`-prefixed. The scripted endpoint, the fake-clock
// Shipper, the diagnostic writer, and the wait helpers are all reused from
// logging/suspend_disable_test.go (newSDServer, newSDShipper, sdBatchSize,
// sdWaitTimeout, sdFlushEvery), so this property observes delivery through
// exactly the same instruments as the example-based tests of the same
// requirements.

import (
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"testing"
	"time"
)

const (
	// p11Settle is how long a "no further request was issued" assertion gives a
	// request that was about to be made to arrive. Polling for zero succeeds
	// instantly and proves nothing, so these assertions pay a small fixed wait.
	// It is shorter than sdSettle because this property pays it once per
	// iteration rather than once per test.
	p11Settle = 10 * waitPollStep

	// p11MaxDraws bounds the draws the end-to-end half makes while looking for
	// servable statuses, so a generator change that stopped producing them fails
	// the test instead of looping forever.
	p11MaxDraws = 40 * pbtIterations
)

// ---------------------------------------------------------------------------
// The oracle: Req 7's classification table
// ---------------------------------------------------------------------------

// p11Expect is what Req 7 says one transmission attempt means: the
// state-machine edge and the single Self_Diagnostic it calls for.
type p11Expect struct {
	Outcome deliveryOutcome
	Diag    diagRequirement
}

func (e p11Expect) String() string {
	return fmt.Sprintf("{outcome: %s, diag: %d}", e.Outcome, e.Diag)
}

// retryable reports whether the requirement makes this outcome eligible for
// retransmission (Req 7.2, 7.3).
func (e p11Expect) retryable() bool { return e.Outcome == outcomeRetry }

// p11ExactStatus is the half of Req 7's table that names statuses
// individually. A map, per the design's P11 row: the keys are exactly the
// statuses the requirement text enumerates, so the table can be read against the
// requirement line by line without tracing the fall-through order of a switch.
var p11ExactStatus = map[int]p11Expect{
	http.StatusAccepted:              {Outcome: outcomeDelivered, Diag: diagNone},        // Req 7.1
	http.StatusPaymentRequired:       {Outcome: outcomeSuspend, Diag: diagErrorQuota},    // Req 7.10
	http.StatusForbidden:             {Outcome: outcomeDisable, Diag: diagErrorToken},    // Req 7.8
	http.StatusNotAcceptable:         {Outcome: outcomeDrop, Diag: diagErrorStatusCount}, // Req 7.11
	http.StatusRequestEntityTooLarge: {Outcome: outcomeDrop, Diag: diagErrorStatusCount}, // Req 7.11
	http.StatusTooManyRequests:       {Outcome: outcomeRetry, Diag: diagNone},            // Req 7.2
}

// p11Oracle is the reference model: what Req 7 requires for one SendOutcome.
//
// It is written from the requirement's ranges rather than from classify.go, and
// its shape is different on purpose — a map lookup for the named statuses, then
// two range comparisons, then the complement — so that an ordering mistake in the
// implementation's switch cannot be mirrored here and pass unnoticed.
func p11Oracle(o SendOutcome) p11Expect {
	// No response at all. Both cases are the input contract of
	// IngestClient.Send rather than anything Req 7's status table covers: a
	// nil Err is the empty-batch no-op that opened no connection (Req 5.12),
	// and any other no-response attempt is a transport failure or an elapsed
	// timeout, which are retryable and deliberately not told apart (Req 7.3).
	if o.Status <= 0 {
		if o.Err == nil {
			return p11Expect{Outcome: outcomeNoRequest, Diag: diagNone}
		}
		return p11Expect{Outcome: outcomeRetry, Diag: diagNone}
	}

	if want, ok := p11ExactStatus[o.Status]; ok {
		return want
	}

	switch {
	case o.Status >= 200 && o.Status <= 299:
		// Req 7.14: any 2xx other than 202 still delivers, plus one WARN.
		return p11Expect{Outcome: outcomeDelivered, Diag: diagWarnStatus}
	case o.Status >= 500 && o.Status <= 599:
		// Req 7.2: the server failed; the same events may be accepted later.
		return p11Expect{Outcome: outcomeRetry, Diag: diagNone}
	default:
		// Req 7.15: everything outside 200-299 that is not named above and not a
		// 5xx — 1xx, 3xx, the other 4xx, and anything at or above 600.
		return p11Expect{Outcome: outcomeDrop, Diag: diagErrorStatus}
	}
}

// p11WantAttempts is the attempt count Property 11 predicts for an endpoint that
// answers status on **every** request.
//
// The property states 1 for the non-retryable set and 2-5 for the retryable one.
// The upper end is exact here rather than a range because the endpoint never
// changes its answer: a retryable status is retried until the ceiling, so the
// batch appears in exactly maxTransmitAttempts requests (Req 7.6, 7.7). A test
// that only asserted "at least 2" would pass for a loop that gave up after the
// second attempt.
func p11WantAttempts(status int) int {
	if p11Oracle(SendOutcome{Status: status}).retryable() {
		return maxTransmitAttempts
	}
	return 1
}

// p11WantDiagLevel is the level Req 7 fixes for the diagnostic an outcome calls
// for: WARN for the delivered-but-not-202 case of Req 7.14, ERROR for every
// other diagnostic Req 7 requires.
func p11WantDiagLevel(d diagRequirement) slog.Level {
	if d == diagWarnStatus {
		return slog.LevelWarn
	}
	return slog.LevelError
}

// ---------------------------------------------------------------------------
// Generated input
// ---------------------------------------------------------------------------

// p11Input is one generated attempt result, as a struct so pbtFailf can render
// it as JSON.
type p11Input struct {
	// Status is the HTTP status the attempt observed, or 0 when no response
	// arrived. It may be negative or far above any real status: a SendOutcome is
	// a Go value, and the classifier must be total over it.
	Status int
	// Failure names the transport failure carried alongside a Status of 0, or is
	// empty when the outcome carries none.
	Failure string
}

// outcome rebuilds the SendOutcome this input describes.
func (in p11Input) outcome() SendOutcome {
	o := SendOutcome{Status: in.Status}
	switch in.Failure {
	case "":
	case "timeout":
		o.Err = fakeTimeoutError{}
	default:
		o.Err = fmt.Errorf("p11: %s", in.Failure)
	}
	return o
}

// p11EdgeInputs are the inputs a random draw must not be trusted to produce,
// checked deterministically in addition to the generated ones.
//
// 199 and 600 are the two statuses Req 7.15 covers that no httptest.Server can
// return, and the whole reason the pure half of this property exists. The rest
// are the values only a hand-built SendOutcome can carry: a negative status, the
// integer extremes, and the two Status-0 cases that Req 5.12 and Req 7.3 pull
// apart.
var p11RequiredStatusInputs = []p11Input{
	{Status: 200},
	{Status: 202},
	{Status: 299},
	{Status: 402},
	{Status: 403},
	{Status: 406},
	{Status: 413},
	{Status: 429},
	{Status: 499},
	{Status: 500},
	{Status: 599},
}

var p11EdgeInputs = []p11Input{
	{Status: 199},
	{Status: 600},
	{Status: 1},
	{Status: 99},
	{Status: 100},
	{Status: 999},
	{Status: math.MaxInt},
	{Status: math.MaxInt32},
	{Status: -1},
	{Status: -503, Failure: "transport"},
	{Status: math.MinInt, Failure: "transport"},
	{Status: 0},                       // Req 5.12: the empty-batch no-op
	{Status: 0, Failure: "transport"}, // Req 7.3: a transport-level failure
	{Status: 0, Failure: "timeout"},   // Req 7.3: an elapsed timeout
}

// p11GenInput draws one attempt result, mostly from genStatus — which the
// harness documents as biased to exactly the boundaries of Req 7's table
// (199/200/202/299/402/403/406/413/429/499/500/599/600) — and occasionally from
// the edge list, so a run samples the unservable and out-of-range values as well
// as the ones a server can produce.
func p11GenInput(r *rand.Rand) p11Input {
	if pbtChance(r, 15) {
		return pbtPick(r, p11EdgeInputs)
	}
	return p11Input{Status: genStatus(r)}
}

// p11Servable reports whether an httptest.Server can answer with this status, so
// the end-to-end half can skip the values only the pure half can reach:
// net/http treats a 1xx as informational rather than final, and a status at or
// above 600 is not transmittable at all.
func p11Servable(status int) bool { return status >= 200 && status <= 599 }

// ---------------------------------------------------------------------------
// Part 1: the pure classifier against the oracle
// ---------------------------------------------------------------------------

// Feature: betterstack-error-logging, Property 11: Status classification determines attempt count
// **Validates: Requirements 7.1, 7.2, 7.3, 7.6, 7.10, 7.11, 7.14, 7.15**
//
// The first half of the property: for an arbitrary status — including the two
// Req 7.15 covers that no server can return — the classifier's verdict, and with
// it the attempt count the verdict implies, is the one Req 7's table gives.
func TestProperty11_ClassifierMatchesTheRequirement7Oracle(t *testing.T) {
	r, seed := pbtRand(t)

	inputs := make([]p11Input, 0, pbtIterations+len(p11EdgeInputs))
	// Seed every status-table boundary before random draws. The oracle and
	// anti-vacuity guards remain unchanged, but their coverage no longer depends
	// on a particular FUNDIS_PBT_SEED happening to draw HTTP 403 (or any other
	// individually named branch).
	inputs = append(inputs, p11RequiredStatusInputs...)
	for i := len(p11RequiredStatusInputs); i < pbtIterations; i++ {
		inputs = append(inputs, p11GenInput(r))
	}
	// The edge list is appended rather than left to chance: a run that happened
	// not to draw 199 or 600 would say nothing about the statuses this half of
	// the property exists for.
	inputs = append(inputs, p11EdgeInputs...)

	var (
		outcomes  = make(map[deliveryOutcome]bool)
		diags     = make(map[diagRequirement]bool)
		statuses  = make(map[int]bool)
		sawUnder  bool // a status below 200
		sawAbove  bool // a status above 599
		sawNegate bool // a negative status
	)

	for i, in := range inputs {
		o := in.outcome()
		want := p11Oracle(o)
		got := classify(o)

		outcomes[want.Outcome] = true
		diags[want.Diag] = true
		statuses[in.Status] = true
		sawUnder = sawUnder || (in.Status > 0 && in.Status < 200)
		sawAbove = sawAbove || in.Status > 599
		sawNegate = sawNegate || in.Status < 0

		if got.outcome != want.Outcome {
			pbtFailf(t, seed, i, in, "classify(%+v).outcome = %s, want %s (Req 7's table)",
				o, got.outcome, want.Outcome)
		}
		if got.diag != want.Diag {
			pbtFailf(t, seed, i, in, "classify(%+v).diag = %d, want %d (Req 7.10, 7.11, 7.14, 7.15)",
				o, got.diag, want.Diag)
		}

		// The derived predicates the delivery loop actually branches on. They are
		// checked against the oracle rather than against got.outcome, so a
		// classification that is wrong *and* self-consistent still fails.
		if got.retriesBatch() != want.retryable() {
			pbtFailf(t, seed, i, in, "retriesBatch() = %t for %+v, want %t (Req 7.2, 7.3)",
				got.retriesBatch(), o, want.retryable())
		}
		if wantDelivered := want.Outcome == outcomeDelivered; got.creditsDelivered() != wantDelivered {
			pbtFailf(t, seed, i, in, "creditsDelivered() = %t for %+v, want %t (Req 7.1, 7.14)",
				got.creditsDelivered(), o, wantDelivered)
		}
		wantDropped := want.Outcome == outcomeDrop || want.Outcome == outcomeSuspend || want.Outcome == outcomeDisable
		if got.creditsDropped() != wantDropped {
			pbtFailf(t, seed, i, in, "creditsDropped() = %t for %+v, want %t (Req 7.10, 7.11, 7.15)",
				got.creditsDropped(), o, wantDropped)
		}
		// A retryable attempt has reached no terminal state, so it may credit
		// neither bucket: crediting one here would double-count against the
		// Req 11.4 identity once the retransmission succeeds.
		if want.retryable() && (got.creditsDelivered() || got.creditsDropped()) {
			pbtFailf(t, seed, i, in, "a retryable %+v credits delivered=%t dropped=%t, want neither (Req 7.2, 7.3, 11.4)",
				o, got.creditsDelivered(), got.creditsDropped())
		}
		if wantDropped && got.dropReason() != reasonNonRetryable {
			pbtFailf(t, seed, i, in, "dropReason() = %v for %+v, want reasonNonRetryable (Req 7.8, 7.10, 7.11, 7.15)",
				got.dropReason(), o)
		}

		// The diagnostic's required content, which Req 7 fixes together with its
		// level: the status is named by Req 7.11, 7.14, and 7.15, the discarded
		// count by Req 7.11 alone.
		if got.diag != diagNone {
			if lvl := got.diag.level(); lvl != p11WantDiagLevel(want.Diag) {
				pbtFailf(t, seed, i, in, "diag level = %v for %+v, want %v (Req 7.11, 7.14, 7.15)",
					lvl, o, p11WantDiagLevel(want.Diag))
			}
		}
		wantNamesStatus := want.Diag == diagWarnStatus || want.Diag == diagErrorStatus || want.Diag == diagErrorStatusCount
		if got.diag.namesStatus() != wantNamesStatus {
			pbtFailf(t, seed, i, in, "namesStatus() = %t for %+v, want %t (Req 7.11, 7.14, 7.15)",
				got.diag.namesStatus(), o, wantNamesStatus)
		}
		if wantNamesCount := want.Diag == diagErrorStatusCount; got.diag.namesCount() != wantNamesCount {
			pbtFailf(t, seed, i, in, "namesCount() = %t for %+v, want %t (Req 7.11)",
				got.diag.namesCount(), o, wantNamesCount)
		}

		// The attempt count the verdict implies, which is what part 2 then
		// observes as HTTP requests (Req 7.6).
		if in.Status > 0 {
			wantAttempts := 1
			if want.retryable() {
				wantAttempts = maxTransmitAttempts
			}
			if got := p11WantAttempts(in.Status); got != wantAttempts {
				pbtFailf(t, seed, i, in, "the implied attempt count for status %d is %d, want %d (Req 7.6)",
					in.Status, got, wantAttempts)
			}
		}
	}

	// Anti-vacuity. Without shrinking, a generator that collapsed onto the 2xx
	// and 5xx interiors would pass this half while exercising none of the
	// individually named statuses, and there would be no counterexample to
	// notice it from.
	for outcome := range map[deliveryOutcome]bool{
		outcomeNoRequest: true, outcomeDelivered: true, outcomeRetry: true,
		outcomeDrop: true, outcomeSuspend: true, outcomeDisable: true,
	} {
		if !outcomes[outcome] {
			t.Errorf("no input classified as %s (seed %d): the oracle's table was only partly exercised", outcome, seed)
		}
	}
	for diag, name := range map[diagRequirement]string{
		diagNone:             "no diagnostic (Req 7.1, 7.2)",
		diagWarnStatus:       "the WARN of Req 7.14",
		diagErrorStatusCount: "the status-and-count ERROR of Req 7.11",
		diagErrorStatus:      "the status ERROR of Req 7.15",
		diagErrorQuota:       "the quota ERROR of Req 7.10",
		diagErrorToken:       "the token ERROR of Req 7.8",
	} {
		if !diags[diag] {
			t.Errorf("no input required %s (seed %d)", name, seed)
		}
	}
	if !statuses[199] || !statuses[600] {
		t.Errorf("199 covered = %t, 600 covered = %t (seed %d): the unservable statuses are the reason this half exists",
			statuses[199], statuses[600], seed)
	}
	if !sawUnder || !sawAbove || !sawNegate {
		t.Errorf("saw a status below 200 = %t, above 599 = %t, negative = %t (seed %d): Req 7.15 is written over the whole complement",
			sawUnder, sawAbove, sawNegate, seed)
	}
	if len(statuses) < 12 {
		t.Errorf("exercised only %d distinct statuses (seed %d), want at least 12", len(statuses), seed)
	}
}

// ---------------------------------------------------------------------------
// Part 2: end-to-end attempt counting
// ---------------------------------------------------------------------------

// Feature: betterstack-error-logging, Property 11: Status classification determines attempt count
// **Validates: Requirements 7.1, 7.2, 7.3, 7.6, 7.10, 7.11, 7.14, 7.15**
//
// The second half: for every status an httptest.Server can actually return, the
// number of HTTP requests one Delivery_Batch produces is 1 for the non-retryable
// set and exactly maxTransmitAttempts for 429 and the 5xx range, which lies in
// the 2-5 the property states.
func TestProperty11_StatusClassificationDeterminesAttemptCount(t *testing.T) {
	r, seed := pbtRand(t)

	var (
		statuses        = make(map[int]bool)
		sawRetryable    bool
		sawNonRetryable bool
		sawFullFive     bool
		skipped         int
	)

	statusesToRun := make([]int, 0, pbtIterations)
	for _, in := range p11RequiredStatusInputs {
		statusesToRun = append(statusesToRun, in.Status)
	}
	randomIterations := pbtIterations - len(statusesToRun)
	for draws, accepted := 0, 0; accepted < randomIterations; draws++ {
		if draws >= p11MaxDraws {
			t.Fatalf("drew %d statuses and found only %d servable ones (seed %d): genStatus must produce 200-599",
				draws, accepted, seed)
		}
		status := genStatus(r)
		if !p11Servable(status) {
			// 199 and 600 are classification inputs, not transmittable
			// responses; part 1 covers them against the pure classifier.
			skipped++
			continue
		}
		statusesToRun = append(statusesToRun, status)
		accepted++
	}

	for i, status := range statusesToRun {
		in := p11Input{Status: status}
		want := p11WantAttempts(status)
		statuses[status] = true
		sawRetryable = sawRetryable || want > 1
		sawNonRetryable = sawNonRetryable || want == 1

		iteration, input := i, in
		// A subtest per iteration, so each iteration's endpoint and delivery
		// goroutine are torn down by its own t.Cleanup rather than accumulating
		// for the length of the run.
		t.Run(fmt.Sprintf("i%03d_status%d", i, status), func(t *testing.T) {
			if got := p11RunAttempts(t, seed, iteration, input); got == maxTransmitAttempts {
				sawFullFive = true
			}
		})
	}

	// Anti-vacuity. Both branches of the property must have been taken — a run
	// that only saw non-retryable statuses would never advance the clock and
	// would prove nothing about retry — and at least one status must have reached
	// the ceiling, which is the assertion a retry loop that gave up early would
	// fail.
	if !sawNonRetryable {
		t.Errorf("no iteration exercised a non-retryable status (seed %d): the attempt-count-is-1 branch was never taken", seed)
	}
	if !sawRetryable {
		t.Errorf("no iteration exercised a retryable status (seed %d): the 429 and 5xx branch was never taken", seed)
	}
	if !sawFullFive {
		t.Errorf("no batch reached %d transmission attempts (seed %d): the retry ceiling of Req 7.6 was never observed",
			maxTransmitAttempts, seed)
	}
	if len(statuses) < 6 {
		t.Errorf("exercised only %d distinct servable statuses (seed %d), want at least 6", len(statuses), seed)
	}
	t.Logf("exercised %d distinct servable statuses; skipped %d unservable draws (covered by part 1)", len(statuses), skipped)
}

// p11RunAttempts drives one Delivery_Batch against an endpoint that always
// answers in.Status, and returns the number of requests the endpoint received.
//
// The batch is triggered by the batch-size rule rather than by a flush interval,
// so the first attempt needs no clock advance at all (Req 5.5); every *later*
// attempt does, which is what the walk below is for.
func p11RunAttempts(t *testing.T, seed int64, iteration int, in p11Input) int {
	t.Helper()

	// One status, repeated for every request: newSDServer answers with the last
	// entry of its script once the script runs out. A constant answer is what
	// makes the retryable expectation exact — the batch is retried to exhaustion
	// rather than rescued by a later 202.
	server := newSDServer(t, in.Status)
	sd := newSDShipper(t, server)

	wantAttempts := p11WantAttempts(in.Status)

	sd.emit(sdBatchSize)

	if !server.waitRequests(1) {
		pbtFatalf(t, seed, iteration, in, "no Delivery_Batch reached the endpoint within %s", sdWaitTimeout)
	}

	if wantAttempts > 1 {
		// **The clock walk, without which this test asserts nothing.** A
		// retryable outcome parks the delivery goroutine in
		// waitBeforeRetransmit -> retryTimer -> clock.After on the injected
		// fakeClock, which fires only when a test advances it. Leaving it alone
		// would leave the goroutine in the first backoff for ever, the endpoint
		// would have received exactly 1 request, and the 2-5 assertion below
		// would fail for a reason that has nothing to do with the Shipper.
		//
		// So for each of the maxTransmitAttempts-1 retransmissions: wait until
		// backoff i has been registered on the clock, then advance by exactly
		// the duration that was registered. The duration is read rather than
		// asserted — the schedule of Req 7.4 and 7.5 is P16's subject (task 8.5)
		// — so this property depends on the *existence* of a backoff and not on
		// its length.
		for retransmission := 1; retransmission < wantAttempts; retransmission++ {
			if !sd.clock.WaitAfters(retransmission, sdWaitTimeout) {
				pbtFatalf(t, seed, iteration, in,
					"only %d backoff(s) were scheduled, want %d: attempt %d never waited (Req 7.2, 7.3)",
					sd.clock.AfterCount(), retransmission, retransmission+1)
			}
			delays := sd.clock.AfterDurations()
			sd.clock.Advance(delays[retransmission-1])

			if !server.waitRequests(retransmission + 1) {
				pbtFatalf(t, seed, iteration, in,
					"the endpoint received %d requests after backoff %d elapsed, want %d (Req 7.2, 7.6)",
					server.requests(), retransmission, retransmission+1)
			}
		}

		// The ceiling is reached by abandonment, not by a sixth attempt
		// (Req 7.6, 7.7).
		if _, ok := sd.waitSnapshot(func(s Snapshot) bool { return s.AbandonedBatches == 1 }); !ok {
			pbtFailf(t, seed, iteration, in,
				"AbandonedBatches = %d after %d retryable attempts, want 1 (Req 7.7) (%+v)",
				sd.Snapshot().AbandonedBatches, wantAttempts, sd.Snapshot())
		}
	} else {
		// A non-retryable status reaches a terminal state on the first attempt:
		// delivered for a 2xx (Req 7.1, 7.14), dropped for everything else
		// (Req 7.10, 7.11, 7.15). Waiting for that rather than for a fixed
		// duration is what keeps the "exactly 1 request" assertion meaningful.
		if _, ok := sd.waitSnapshot(func(s Snapshot) bool {
			return s.Delivered >= sdBatchSize || s.Dropped >= sdBatchSize
		}); !ok {
			pbtFatalf(t, seed, iteration, in,
				"the batch reached no terminal state within %s (%+v)", sdWaitTimeout, sd.Snapshot())
		}
		// A flush boundary must not resurrect it: the batch was discarded, not
		// deferred. 402 suspends for 15 minutes, so one interval cannot resume
		// transmission either (Req 7.10).
		sd.clock.Advance(sdFlushEvery)
	}

	// Give a further request the chance to arrive before counting, so "no sixth
	// attempt" and "no retransmission" are claims about a settled system.
	time.Sleep(p11Settle)

	got := server.requests()

	// The property. The lower bound is stated as the requirement states it —
	// 1 exactly, or within 2-5 — and the upper bound of the retryable case is
	// exact because the endpoint never changed its answer.
	switch {
	case wantAttempts == 1 && got != 1:
		pbtFailf(t, seed, iteration, in,
			"HTTP %d produced %d transmission attempts, want exactly 1: the status is not retryable (Req 7.1, 7.10, 7.11, 7.14, 7.15)",
			in.Status, got)
	case wantAttempts > 1 && (got < 2 || got > maxTransmitAttempts):
		pbtFailf(t, seed, iteration, in,
			"HTTP %d produced %d transmission attempts, want between 2 and %d (Req 7.2, 7.6)",
			in.Status, got, maxTransmitAttempts)
	case wantAttempts > 1 && got != maxTransmitAttempts:
		pbtFailf(t, seed, iteration, in,
			"HTTP %d produced %d transmission attempts, want exactly %d: an endpoint that always answers retryably is retried to exhaustion (Req 7.6, 7.7)",
			in.Status, got, maxTransmitAttempts)
	}

	// The accounting identity holds at the end of every iteration, whichever
	// branch it took (Req 11.4).
	assertCounterIdentities(t, fmt.Sprintf("after HTTP %d produced %d attempt(s)", in.Status, got), sd.Snapshot())

	return got
}
