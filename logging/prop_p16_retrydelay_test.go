package logging

// Property 16: Retry delays stay within the jitter bounds.
//
// *For any* sequence of retryable responses, including 429 responses carrying
// arbitrary `Retry-After` header values, every observed retry delay lies in the
// closed interval from the base retry delay to 1.2 times the base retry delay and
// never exceeds 360 seconds, where the base follows the doubling schedule capped
// at 30 seconds or the `Retry-After` value clamped to 1-300 seconds (Req 7.4,
// 7.5).
//
// The property is asserted twice, over the same oracle, because the two halves
// can fail independently:
//
//  1. **The pure computation.** retryDelay over arbitrary attempt numbers and
//     arbitrary Retry-After values, with arbitrary Jitter implementations —
//     including hostile ones that violate the [0, 1) contract, which is the case
//     the 360 s ceiling in retryDelay exists for.
//  2. **End to end through the delivery loop.** A Shipper driven against a server
//     answering a generated retryable status with a generated `Retry-After`
//     header, with the whole backoff schedule read back from
//     fakeClock.AfterDurations(). This is the half that would catch a delivery
//     loop which computed a correct delay and then waited a different one, or
//     which passed a 5xx's Retry-After header through to the schedule.
//
// **The oracle is derived from Req 7.4's words, not from baseRetryDelay.**
// p16OracleBase re-derives the schedule by repeated doubling with a clamp and
// re-derives the Retry-After clamp from the literal bounds 1 s and 300 s, and it
// uses its own constants rather than the production ones. Calling baseRetryDelay
// would make the expectation and the implementation the same statement, and a
// wrong schedule would satisfy the property perfectly.
//
// **Where the 429-only rule enters.** Req 7.4 honours the header for HTTP 429
// alone, so the oracle decides "honoured" from the *status* as well as the value,
// and the generators deliberately produce 5xx responses carrying a usable
// `Retry-After`. A 5xx whose header is honoured shows up as a recorded delay far
// above 1.2 x its doubling base, which p16Violation reports — and
// p16HonouredHeaderViolation names it directly, so the failure says "the header
// was honoured for a 503" instead of "delay out of range".
//
// **The one deliberate reading in the oracle: a `Retry-After: 0`.** Req 7.4 clamps
// the header "to the range 1 through 300 seconds", and 0 parses as a non-negative
// integer, so a literal reading would put the base at 1 s. The implementation
// cannot distinguish that case: parseRetryAfter yields a zero duration for an
// absent, malformed, *and* zero-valued header, and retryDelay treats a zero as
// "no header" and falls back to the doubling schedule. The oracle follows the
// implementation here — honoured means strictly positive — for two reasons: the
// distinction is unobservable at the seam Req 7.4 describes, and the resulting
// delay is never *shorter* than the 1 s floor the clamp would have imposed,
// because every base on the doubling schedule is at least 1 s. Rather than sweep
// the case away, the delivery half generates `Retry-After: 0` on purpose and
// asserts that weaker floor claim explicitly (see p16FloorViolation), so the
// reading is tested rather than assumed. The 1 s floor of the clamp proper is
// exercised by sub-second values in the pure half, which is the only place a
// Retry-After below one second can arise at all — the header carries whole
// seconds.
//
// **Anti-vacuity.** With no shrinking to lean on, a property that never reached
// its boundaries would pass while proving nothing, so both halves accumulate
// coverage flags and assert them at the end: the doubling schedule walked to the
// attempt ceiling, the 30 s cap reached, the Retry-After clamp exercised at both
// ends, a 5xx carrying a usable header, and a delay that jitter actually
// lengthened. The 30 s cap is reachable in the pure half only: the schedule
// reaches 30 s at attempt 6, and the 5-attempt ceiling of Req 7.6 means the
// delivery loop never waits a fifth time. p16Violation and the two named checks
// are pure functions of (delay, base), and TestP16ViolationDetectsOutOfBoundDelays
// feeds them synthetic delays, so a green run is evidence about the bounds rather
// than evidence that the guard is unreachable.
//
// File naming: task 8.2 owns logging/retry_test.go and task 8.4 is adding
// Property 11 to it, so this property lives in its own file and every identifier
// it introduces is prefixed p16. The helpers it reuses — newRetryTestShipper,
// hostileJitter, newSDServer, deliveryConfig, deliveryPaddedEvent — are declared
// there and in the delivery test files, and are deliberately not redeclared.

import (
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The oracle: Req 7.4 and 7.5 restated from their own words
// ---------------------------------------------------------------------------

// These constants are the requirement's numbers, spelled out here rather than
// imported from logging/retry.go. A test that reused maxBaseRetryDelay could not
// notice the cap being retuned; this one fails.
const (
	// p16BaseUnit is Req 7.4's "1 second before the first retransmission".
	p16BaseUnit = 1 * time.Second
	// p16BaseCap is Req 7.4's "up to a maximum of 30 seconds".
	p16BaseCap = 30 * time.Second
	// p16RetryAfterFloor and p16RetryAfterCeiling are Req 7.4's "clamped to the
	// range 1 through 300 seconds".
	p16RetryAfterFloor   = 1 * time.Second
	p16RetryAfterCeiling = 300 * time.Second
	// p16DelayCeiling is Req 7.5's "never exceeds 360 seconds".
	p16DelayCeiling = 360 * time.Second
)

// p16OracleBase returns the base retry delay Req 7.4 prescribes before the
// retransmission that follows `attempt` transmissions, for a response whose
// honoured Retry-After value is retryAfter (zero when there is none).
//
// The doubling is computed by *repeated doubling with a clamp*, which is the
// requirement's own phrasing — "1 second before the first retransmission,
// [double] before each subsequent retransmission up to a maximum of 30 seconds" —
// rather than by the shift-and-cap arithmetic the implementation uses. Two
// independent formulations of the same table is the whole point: a bug in the
// production schedule cannot be mirrored here.
//
// The loop is bounded regardless of how large attempt is, because it returns as
// soon as the cap is reached, which takes at most five doublings. A non-positive
// attempt is read as the first retransmission, which is the only delay a caller
// that has transmitted nothing could sensibly be asking for.
func p16OracleBase(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		// Req 7.4: the honoured header replaces the computed base entirely.
		switch {
		case retryAfter < p16RetryAfterFloor:
			return p16RetryAfterFloor
		case retryAfter > p16RetryAfterCeiling:
			return p16RetryAfterCeiling
		default:
			return retryAfter
		}
	}

	if attempt < 1 {
		attempt = 1
	}
	base := p16BaseUnit
	for step := 1; step < attempt; step++ {
		if base >= p16BaseCap {
			return p16BaseCap
		}
		base *= 2
		if base > p16BaseCap {
			return p16BaseCap
		}
	}
	return base
}

// p16UpperBound returns 1.2 times base, computed in integer nanoseconds as
// base + base/5.
//
// Integer arithmetic rather than a float multiply, so the bound itself carries no
// rounding of its own: every base the oracle can produce is a whole number of
// nanoseconds, and the implementation's own jitter term is a truncated product
// strictly below 0.2 x base, so it can never exceed base/5. A float bound would
// leave the assertion's last nanosecond up to the order of two multiplications.
func p16UpperBound(base time.Duration) time.Duration { return base + base/5 }

// p16Violation reports how delay breaks Req 7.5 for the given base, or "" when it
// holds. It is the whole property, as a pure function, so
// TestP16ViolationDetectsOutOfBoundDelays can prove it fires.
func p16Violation(delay, base time.Duration) string {
	if delay < base {
		return fmt.Sprintf("delay %v is below the base retry delay %v: jitter is added to the base, never subtracted (Req 7.5)", delay, base)
	}
	if upper := p16UpperBound(base); delay > upper {
		return fmt.Sprintf("delay %v exceeds 1.2 x the base retry delay %v = %v: jitter is at most 20 percent of the base (Req 7.5)", delay, base, upper)
	}
	if delay > p16DelayCeiling {
		return fmt.Sprintf("delay %v exceeds the absolute maximum %v (Req 7.5)", delay, p16DelayCeiling)
	}
	return ""
}

// p16HonouredHeaderViolation reports a delay that can only be explained by a
// Retry-After header having been honoured for a status other than 429, which
// Req 7.4 restricts to 429 alone.
//
// It fires only when the two schedules are actually distinguishable — when the
// clamped header value lies above the jitter bound of the doubling base — so a
// header that happens to agree with the schedule produces no false accusation.
// p16Violation would catch the same delay; this check exists to name the cause.
func p16HonouredHeaderViolation(status int, delay, base, headerBase time.Duration) string {
	if headerBase <= p16UpperBound(base) || delay < headerBase {
		return ""
	}
	return fmt.Sprintf("delay %v reached the clamped Retry-After value %v for HTTP %d, whose base is %v: the header is honoured for 429 alone (Req 7.4)",
		delay, headerBase, status, base)
}

// p16FloorViolation reports a delay below the 1 s floor Req 7.4's clamp names.
//
// It is the claim the delivery half makes about a `Retry-After: 0`, which the
// implementation cannot distinguish from an absent header (see the file comment):
// whatever schedule such a response follows, the wait is never shorter than the
// floor the clamp would have imposed.
func p16FloorViolation(delay time.Duration) string {
	if delay >= p16RetryAfterFloor {
		return ""
	}
	return fmt.Sprintf("delay %v is below the %v floor Req 7.4 clamps a Retry-After value to", delay, p16RetryAfterFloor)
}

// p16HeaderRetryAfter returns the Retry-After value Req 7.4 honours for a
// response with the given status and raw header value, or 0 when there is none to
// honour.
//
// It parses the header independently of parseRetryAfter, again so that the
// expectation is not the implementation. Only the delta-seconds form is
// recognised, which is the form Req 7.4 names; an HTTP-date, a malformed value,
// and a negative count all yield 0, as does every status other than 429.
//
// The 1-300 s clamp is applied in *seconds* space before the conversion to a
// Duration, which matters for a header of several gigaseconds: multiplying that
// by time.Second would overflow int64 and wrap to a negative duration, and the
// oracle would then disagree with the implementation for a reason that has
// nothing to do with the property. Clamping first is exact — every value above
// 300 collapses to 300 either way.
func p16HeaderRetryAfter(status int, header string) time.Duration {
	if status != http.StatusTooManyRequests {
		return 0
	}
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds < 0 {
		return 0
	}
	if seconds > int(p16RetryAfterCeiling/time.Second) {
		return p16RetryAfterCeiling
	}
	return time.Duration(seconds) * time.Second
}

// ---------------------------------------------------------------------------
// Generators: the pure half
// ---------------------------------------------------------------------------

// p16DrawsPerInput is how many delays are drawn per generated input. It is above
// 1 so a randomised Jitter is sampled repeatedly against the same base rather
// than once, which is what makes an occasional out-of-range fraction likely to be
// seen at all.
const p16DrawsPerInput = 8

// p16AttemptBoundaries are the attempt numbers where the schedule changes
// behaviour: every step of the enumerated table, the first attempt whose doubled
// base would reach the 30 s cap, and the one after it.
var p16AttemptBoundaries = []int{1, 2, 3, 4, 5, 6, 7}

// p16AttemptExtremes are the attempts no caller in this package produces but a
// pure function must still answer: below the first transmission, far past the
// 5-attempt ceiling, and the values where `1 << (attempt-1)` overflows.
var p16AttemptExtremes = []int{0, -1, 8, 12, 64, 1000, math.MaxInt32, math.MaxInt, math.MinInt}

// p16RetryAfterBoundaries straddle both ends of the Req 7.4 clamp: absent,
// negative, one nanosecond, either side of the 1 s floor, values inside the
// range, either side of the 300 s ceiling, and values so large that only a clamp
// keeps them usable.
var p16RetryAfterBoundaries = []time.Duration{
	0,
	-1,
	-5 * time.Second,
	1 * time.Nanosecond,
	500 * time.Millisecond,
	999 * time.Millisecond,
	1 * time.Second,
	1*time.Second + 1,
	2 * time.Second,
	7 * time.Second,
	120 * time.Second,
	299 * time.Second,
	300 * time.Second,
	301 * time.Second,
	1000 * time.Second,
	// The ingest client's own arithmetic cap of one day, and the largest
	// duration there is: both collapse to the 300 s ceiling.
	86400 * time.Second,
	time.Duration(math.MaxInt64),
}

// p16Statuses are the statuses a retryable outcome can carry, plus 0 for a
// transport-level failure, which has no header to honour at all (Req 7.3).
var p16Statuses = []int{http.StatusTooManyRequests, 500, 502, 503, 599, 0}

// p16Input is one generated input to the pure half.
//
// The Jitter is reported by label rather than by value: a hostile one holds a NaN
// or an infinity, which json.Marshal rejects outright, and a counterexample must
// never be lost to its own reporting.
type p16Input struct {
	// Status is the status the response carried, which decides whether the
	// header is honoured at all (Req 7.4).
	Status int
	// RetryAfter is the raw, unclamped value SendOutcome carried.
	RetryAfter time.Duration
	// Attempt is how many times the batch has been transmitted.
	Attempt int
	// Jitter labels the Jitter implementation in use.
	Jitter string

	jitter Jitter
}

// p16GenInput draws one input for the pure half.
func p16GenInput(r *rand.Rand) p16Input {
	jitter, label := p16GenJitter(r)
	return p16Input{
		Status:     pbtPick(r, p16Statuses),
		RetryAfter: p16GenRetryAfter(r),
		Attempt:    p16GenAttempt(r),
		Jitter:     label,
		jitter:     jitter,
	}
}

// p16GenInputs draws the inputs for a whole run: pbtIterations random ones,
// followed by the handful of inputs the run's coverage claims are about.
//
// The forced tail is not decoration. Every boundary below is reachable by the
// random draws, but only with a probability that depends on the seed — the 300 s
// clamp *and* a jitter fraction near 1 in the same iteration is a few percent —
// so a run under FUNDIS_PBT_SEED could legitimately miss one and fail on its
// coverage assertion rather than on the property. Appending them makes the
// coverage deterministic while leaving every random draw untouched.
func p16GenInputs(r *rand.Rand) []p16Input {
	inputs := make([]p16Input, 0, pbtIterations+8)
	for i := 0; i < pbtIterations; i++ {
		inputs = append(inputs, p16GenInput(r))
	}
	return append(inputs,
		// The 1 s base, exactly, with no jitter at all.
		p16Input{Status: 500, Attempt: 1, Jitter: "FixedJitter(0)", jitter: FixedJitter(0)},
		// A doubled base between the unit and the cap.
		p16Input{Status: 503, Attempt: 3, Jitter: "FixedJitter(0.5)", jitter: FixedJitter(0.5)},
		// The 30 s cap, which no delivery loop can reach: the schedule reaches it
		// at attempt 6 and Req 7.6 stops at 5.
		p16Input{Status: 500, Attempt: 6, Jitter: "FixedJitter(0.5)", jitter: FixedJitter(0.5)},
		// The floor of the clamp: a sub-second value, which only the pure half can
		// present, since the header carries whole seconds.
		p16Input{Status: http.StatusTooManyRequests, RetryAfter: 500 * time.Millisecond, Attempt: 4,
			Jitter: "FixedJitter(0)", jitter: FixedJitter(0)},
		// Inside the clamp.
		p16Input{Status: http.StatusTooManyRequests, RetryAfter: 7 * time.Second, Attempt: 2,
			Jitter: "RandJitter", jitter: RandJitter(16)},
		// The ceiling of the clamp with the largest jitter a Jitter may report, so
		// the delay lands just under the 360 s ceiling of Req 7.5.
		p16Input{Status: http.StatusTooManyRequests, RetryAfter: 1000 * time.Second, Attempt: 1,
			Jitter: "FixedJitter(1) clamped to just below 1", jitter: FixedJitter(1)},
		// The same ceiling reached through a hostile Jitter, which is the case the
		// 360 s clamp in retryDelay exists for.
		p16Input{Status: http.StatusTooManyRequests, RetryAfter: 301 * time.Second, Attempt: 5,
			Jitter: "hostileJitter(5000)", jitter: hostileJitter{f: 5000}},
		// A 5xx carrying a Retry-After the schedule must ignore (Req 7.4).
		p16Input{Status: 503, RetryAfter: 120 * time.Second, Attempt: 1,
			Jitter: "FixedJitter(0)", jitter: FixedJitter(0)},
	)
}

// p16GenAttempt draws a transmission-attempt count, biased toward the steps of
// the schedule and the cap boundary.
func p16GenAttempt(r *rand.Rand) int {
	switch {
	case pbtChance(r, 65):
		return pbtPick(r, p16AttemptBoundaries)
	case pbtChance(r, 40):
		return pbtPick(r, p16AttemptExtremes)
	default:
		return 1 + r.Intn(12)
	}
}

// p16GenRetryAfter draws a raw Retry-After value, biased toward both ends of the
// clamp and toward having no header at all.
func p16GenRetryAfter(r *rand.Rand) time.Duration {
	switch {
	case pbtChance(r, 25):
		return 0 // no usable header: the doubling schedule applies
	case pbtChance(r, 70):
		return pbtPick(r, p16RetryAfterBoundaries)
	default:
		return time.Duration(r.Int63n(int64(400 * time.Second)))
	}
}

// p16GenJitter draws a Jitter together with the label a counterexample reports.
//
// The hostile entries are the reason the 360 s ceiling and the "never below the
// base" guard exist in retryDelay: a Jitter supplied from outside this package
// can report anything at all, and Req 7.5's bound has to hold anyway. hostileJitter
// is declared in logging/retry_test.go (task 8.2) for exactly this purpose.
func p16GenJitter(r *rand.Rand) (Jitter, string) {
	if pbtChance(r, 20) {
		hostile := pbtPick(r, []float64{
			1, 1.5, 5000, 1e18, math.Inf(1), math.NaN(), -1, -0.5, math.Inf(-1),
		})
		return hostileJitter{f: hostile}, fmt.Sprintf("hostileJitter(%v)", hostile)
	}
	switch r.Intn(6) {
	case 0:
		return FixedJitter(0), "FixedJitter(0)"
	case 1:
		return FixedJitter(0.5), "FixedJitter(0.5)"
	case 2:
		return FixedJitter(0.999999), "FixedJitter(0.999999)"
	case 3:
		return FixedJitter(1), "FixedJitter(1) clamped to just below 1"
	case 4:
		seed := r.Int63()
		return RandJitter(seed), fmt.Sprintf("RandJitter(%d)", seed)
	default:
		return RandJitter(r.Int63()), "RandJitter"
	}
}

// ---------------------------------------------------------------------------
// The pure half
// ---------------------------------------------------------------------------

// Feature: betterstack-error-logging, Property 16: Retry delays stay within the jitter bounds
// **Validates: Requirements 7.4, 7.5**
func TestProperty16_RetryDelaysStayWithinTheJitterBounds(t *testing.T) {
	r, seed := pbtRand(t)

	var (
		sawFirstBase   bool
		sawDoubling    bool
		sawBaseCap     bool
		sawFloorClamp  bool
		sawInsideClamp bool
		sawCeilClamp   bool
		sawIgnored5xx  bool
		sawExactBase   bool
		sawJittered    bool
		sawNearCeiling bool
	)

	for i, in := range p16GenInputs(r) {
		s, _, _ := newRetryTestShipper(t, in.jitter)

		// The delivery loop routes every Retry-After through retryAfterForStatus,
		// so the property is asserted over the same composition rather than over
		// retryDelay with a value no status could have supplied. The 429-only rule
		// of Req 7.4 therefore enters the expectation as well: the oracle's base
		// uses the honoured value, which is zero for every other status.
		honoured := retryAfterForStatus(in.Status, in.RetryAfter)
		base := p16OracleBase(in.Attempt, honoured)

		// What the header *would* have produced had it been honoured, whatever the
		// status: the input to the 429-only check below. For a non-positive value
		// there is nothing to honour, and p16OracleBase yields the schedule, which
		// makes the check stand down.
		headerBase := p16OracleBase(in.Attempt, in.RetryAfter)

		for draw := 0; draw < p16DrawsPerInput; draw++ {
			delay := s.retryDelay(in.Attempt, honoured)

			if v := p16Violation(delay, base); v != "" {
				pbtFailf(t, seed, i, in, "%s", v)
				break
			}
			if v := p16FloorViolation(delay); v != "" {
				pbtFailf(t, seed, i, in, "%s", v)
				break
			}
			if in.RetryAfter > 0 {
				if v := p16HonouredHeaderViolation(in.Status, delay, base, headerBase); v != "" {
					pbtFailf(t, seed, i, in, "%s", v)
					break
				}
			}

			sawExactBase = sawExactBase || delay == base
			sawJittered = sawJittered || delay > base
			sawNearCeiling = sawNearCeiling || delay > p16DelayCeiling-time.Second
		}

		honouredHeader := honoured > 0
		sawFirstBase = sawFirstBase || (!honouredHeader && base == p16BaseUnit)
		sawDoubling = sawDoubling || (!honouredHeader && base > p16BaseUnit && base < p16BaseCap)
		sawBaseCap = sawBaseCap || (!honouredHeader && base == p16BaseCap)
		sawFloorClamp = sawFloorClamp || (honouredHeader && in.RetryAfter < p16RetryAfterFloor)
		sawInsideClamp = sawInsideClamp ||
			(honouredHeader && in.RetryAfter >= p16RetryAfterFloor && in.RetryAfter <= p16RetryAfterCeiling)
		sawCeilClamp = sawCeilClamp || (honouredHeader && in.RetryAfter > p16RetryAfterCeiling)
		sawIgnored5xx = sawIgnored5xx ||
			(in.Status != http.StatusTooManyRequests && in.RetryAfter > 0 && headerBase > p16UpperBound(base))
	}

	p16AssertCoverage(t, seed, []p16Coverage{
		{sawFirstBase, "the 1 s base before the first retransmission (Req 7.4)"},
		{sawDoubling, "a doubled base between 1 s and the 30 s cap (Req 7.4)"},
		{sawBaseCap, "the 30 s cap on the doubling schedule (Req 7.4)"},
		{sawFloorClamp, "a Retry-After value below the 1 s floor of the clamp (Req 7.4)"},
		{sawInsideClamp, "a Retry-After value inside the 1-300 s range (Req 7.4)"},
		{sawCeilClamp, "a Retry-After value above the 300 s ceiling of the clamp (Req 7.4)"},
		{sawIgnored5xx, "a non-429 status carrying a Retry-After the schedule must ignore (Req 7.4)"},
		{sawExactBase, "a delay of exactly the base, which zero jitter produces (Req 7.5)"},
		{sawJittered, "a delay that jitter lengthened beyond the base (Req 7.5)"},
		{sawNearCeiling, "a delay within one second of the 360 s ceiling (Req 7.5)"},
	})
}

// ---------------------------------------------------------------------------
// Generators and harness: the delivery half
// ---------------------------------------------------------------------------

// p16WaitTimeout bounds every wait for the delivery goroutine to reach a state.
// It is only ever waited out by a failing iteration.
const p16WaitTimeout = 2 * time.Second

// p16FlushEvery is long enough that no flush tick can transmit anything by
// accident: every request in the delivery half is caused by the batch-size
// trigger or by a retransmission. Advancing the clock through a 300 s backoff
// crosses it, which is harmless — a tick with an empty pending transmits nothing
// (Req 5.12).
const p16FlushEvery = 60 * time.Second

// p16QueueSize is the Event_Queue capacity. One event per iteration is enough:
// the property is about the *schedule*, and a batch of one reaches the wire on
// the batch-size trigger with no interval elapsing.
const p16QueueSize = 4

// p16HeaderValues are the raw Retry-After header values the delivery half
// generates. Every one of them is a case Req 7.4 has an answer for:
//
//	""            no header at all
//	"0"           the value the implementation cannot distinguish from absent
//	"1" .. "300"  inside the clamp, including both ends
//	"301", "1000" above the ceiling
//	"9999999999"  above the ingest client's own one-day cap
//	the last two  values that do not parse as delta-seconds
var p16HeaderValues = []string{
	"", "0", "1", "2", "7", "120", "299", "300", "301", "1000", "9999999999",
	"-5", "Wed, 21 Oct 2015 07:28:00 GMT",
}

// p16Delivery is one generated input to the delivery half.
type p16Delivery struct {
	// Status is the retryable status the endpoint answers with, on every
	// attempt, so the batch runs to the ceiling of Req 7.6.
	Status int
	// Header is the raw Retry-After value every response carries.
	Header string
	// Jitter labels the Jitter implementation in use.
	Jitter string

	jitter Jitter
}

// p16GenDelivery draws one delivery input. Statuses are restricted to the ones
// classification finds retryable — a terminal status would produce no backoff at
// all, and status 0 cannot be served by an httptest.Server.
func p16GenDelivery(r *rand.Rand) p16Delivery {
	jitter, label := p16GenJitter(r)

	status := http.StatusTooManyRequests
	if !pbtChance(r, 50) {
		status = pbtPick(r, []int{500, 502, 503, 599})
	}
	header := ""
	if pbtChance(r, 80) {
		header = pbtPick(r, p16HeaderValues)
	}
	return p16Delivery{Status: status, Header: header, Jitter: label, jitter: jitter}
}

// p16GenDeliveries draws the delivery half's inputs: pbtIterations random ones
// followed by the cases its coverage claims are about, for the same reason
// p16GenInputs has a forced tail — a coverage assertion that depended on the seed
// would fail for a reason unrelated to the property.
func p16GenDeliveries(r *rand.Rand) []p16Delivery {
	inputs := make([]p16Delivery, 0, pbtIterations+6)
	for i := 0; i < pbtIterations; i++ {
		inputs = append(inputs, p16GenDelivery(r))
	}
	return append(inputs,
		// The whole doubling schedule, exactly, with no header and no jitter.
		p16Delivery{Status: 500, Jitter: "FixedJitter(0)", jitter: FixedJitter(0)},
		// The same schedule with jitter, so every recorded delay is strictly
		// inside the interval rather than at its floor.
		p16Delivery{Status: 503, Jitter: "FixedJitter(0.5)", jitter: FixedJitter(0.5)},
		// A 429 whose header replaces the schedule.
		p16Delivery{Status: http.StatusTooManyRequests, Header: "7", Jitter: "RandJitter", jitter: RandJitter(29)},
		// A 429 carrying the value the implementation cannot distinguish from an
		// absent header. The floor claim is what this iteration is for.
		p16Delivery{Status: http.StatusTooManyRequests, Header: "0", Jitter: "FixedJitter(0)", jitter: FixedJitter(0)},
		// A 429 whose header is clamped down to the 300 s ceiling, with the largest
		// jitter a Jitter may report: the recorded delays land just under 360 s.
		p16Delivery{Status: http.StatusTooManyRequests, Header: "1000",
			Jitter: "FixedJitter(1) clamped to just below 1", jitter: FixedJitter(1)},
		// A 5xx carrying a usable header, which the recorded schedule must ignore.
		p16Delivery{Status: 503, Header: "120", Jitter: "FixedJitter(0)", jitter: FixedJitter(0)},
	)
}

// honoured returns the Retry-After value Req 7.4 honours for this response, which
// is zero for every status but 429.
func (in p16Delivery) honoured() time.Duration {
	return p16HeaderRetryAfter(in.Status, in.Header)
}

// headerBase returns the base the header *would* have produced had it been
// honoured, regardless of status, so an implementation that honoured it for a
// 5xx can be named.
func (in p16Delivery) headerBase() time.Duration {
	return p16HeaderRetryAfter(http.StatusTooManyRequests, in.Header)
}

// p16StartShipper builds and starts a Shipper aimed at a loopback endpoint with
// an exact jitter source, and returns it with a function that releases its
// delivery goroutine.
//
// The stop function is called at the end of each iteration rather than left to
// cleanup, so a hundred iterations hold one delivery goroutine at a time instead
// of a hundred. It is a sync.OnceFunc and is also registered as a cleanup, so a
// failing iteration that returns early still releases the goroutine and a double
// call cannot close closeDone twice.
//
// The Config comes from deliveryConfig, so every field carries the value
// ResolveConfig would have produced; only the flush interval, the queue size, and
// the jitter are overridden, and each for a reason named at its constant.
func p16StartShipper(t *testing.T, url string, jitter Jitter, clock *fakeClock) (*Shipper, func()) {
	t.Helper()

	cfg := deliveryConfig(url, 1, p16QueueSize, clock)
	cfg.FlushEvery = p16FlushEvery
	cfg.Jitter = jitter

	s := NewShipper(cfg)
	s.Start()
	// closeDone is the edge run itself selects on; Close is task 8.8's.
	stop := sync.OnceFunc(func() { close(s.closeDone) })
	t.Cleanup(stop)

	if !clock.WaitTickers(1, p16WaitTimeout) {
		t.Fatalf("the delivery goroutine registered no flush ticker within %s", p16WaitTimeout)
	}
	return s, stop
}

// Feature: betterstack-error-logging, Property 16: Retry delays stay within the jitter bounds
// **Validates: Requirements 7.4, 7.5**
func TestProperty16_RetryDelaySchedulesRecordedByTheDeliveryLoopStayWithinTheBounds(t *testing.T) {
	r, seed := pbtRand(t)

	// No iteration may leak its delivery goroutine. Registered before the loop:
	// cleanups run last-in-first-out, so this one runs after every per-iteration
	// stop rather than before it.
	baseline := stableGoroutineCount()
	t.Cleanup(func() {
		if !waitFor(func() bool { return runtime.NumGoroutine() <= baseline }, p16WaitTimeout) {
			t.Errorf("goroutine count = %d after %d iterations, want at most the baseline %d",
				runtime.NumGoroutine(), pbtIterations, baseline)
		}
	})

	var (
		sawFullSchedule bool
		sawHonoured429  bool
		sawZeroHeader   bool
		sawCeilClamp    bool
		sawIgnored5xx   bool
		sawJittered     bool
		sawExactBase    bool
	)

	for i, in := range p16GenDeliveries(r) {
		recorded := p16RunDelivery(t, seed, i, in)

		honoured := in.honoured()
		sawFullSchedule = sawFullSchedule || (honoured == 0 && len(recorded) == maxTransmitAttempts-1)
		sawHonoured429 = sawHonoured429 || honoured > 0
		sawZeroHeader = sawZeroHeader || (in.Status == http.StatusTooManyRequests && in.Header == "0")
		sawCeilClamp = sawCeilClamp || honoured == p16RetryAfterCeiling
		sawIgnored5xx = sawIgnored5xx || (in.Status != http.StatusTooManyRequests &&
			in.headerBase() > p16UpperBound(p16OracleBase(1, 0)))

		for attempt, delay := range recorded {
			base := p16OracleBase(attempt+1, honoured)
			sawExactBase = sawExactBase || delay == base
			sawJittered = sawJittered || delay > base
		}
	}

	p16AssertCoverage(t, seed, []p16Coverage{
		{sawFullSchedule, "a batch whose whole doubling schedule was waited out to the 5-attempt ceiling (Req 7.4, 7.6)"},
		{sawHonoured429, "a 429 whose Retry-After header replaced the schedule (Req 7.4)"},
		{sawZeroHeader, "a 429 carrying Retry-After: 0, which the 1 s floor claim covers (Req 7.4)"},
		{sawCeilClamp, "a 429 whose Retry-After was clamped down to 300 s (Req 7.4)"},
		{sawIgnored5xx, "a 5xx carrying a Retry-After the recorded schedule must ignore (Req 7.4)"},
		{sawExactBase, "a recorded delay of exactly the base, which zero jitter produces (Req 7.5)"},
		{sawJittered, "a recorded delay that jitter lengthened beyond the base (Req 7.5)"},
	})
}

// p16RunDelivery drives one generated input through a real delivery goroutine and
// checks every delay the fake clock recorded against the oracle. It returns the
// recorded schedule so the caller can accumulate coverage.
//
// The walk is the only way to make progress: the delivery goroutine is parked in
// its backoff until the clock reaches the deadline, so each delay is read from the
// recorder first and then advanced through — which is also what proves the loop
// waited the delay it computed rather than some other duration.
func p16RunDelivery(t *testing.T, seed int64, iteration int, in p16Delivery) []time.Duration {
	t.Helper()

	// One retryable status for every attempt, so the batch runs to the ceiling.
	server := newSDServer(t, in.Status).withRetryAfter(in.Header)
	defer server.Close() // idempotent; cleanup closes it again at test end

	clock := newFakeClock()
	s, stop := p16StartShipper(t, server.URL, in.jitter, clock)
	defer stop()

	// Enqueued directly, with the admission counted the way Handle counts it. The
	// event's content is irrelevant to a schedule; its being a batch of one is
	// what makes the first attempt immediate.
	s.counters.addAdmitted(1)
	s.enqueue(deliveryPaddedEvent(t, deliveryMessage("p16", iteration), 0))

	honoured := in.honoured()
	headerBase := in.headerBase()

	recorded := make([]time.Duration, 0, maxTransmitAttempts-1)
	for attempt := 1; attempt < maxTransmitAttempts; attempt++ {
		if !clock.WaitAfters(attempt, p16WaitTimeout) {
			pbtFatalf(t, seed, iteration, in,
				"only %d backoff(s) were scheduled on the injected clock, want %d: attempt %d never waited (Req 7.2, 12.2); schedule so far %v",
				clock.AfterCount(), attempt, attempt+1, clock.AfterDurations())
		}

		delay := clock.AfterDurations()[attempt-1]
		base := p16OracleBase(attempt, honoured)

		if v := p16Violation(delay, base); v != "" {
			pbtFailf(t, seed, iteration, in, "backoff %d of the recorded schedule %v: %s",
				attempt, clock.AfterDurations(), v)
		}
		if v := p16FloorViolation(delay); v != "" {
			pbtFailf(t, seed, iteration, in, "backoff %d: %s", attempt, v)
		}
		if v := p16HonouredHeaderViolation(in.Status, delay, base, headerBase); v != "" {
			pbtFailf(t, seed, iteration, in, "backoff %d: %s", attempt, v)
		}

		recorded = append(recorded, delay)
		clock.Advance(delay)
	}

	// The fifth attempt ends retryably, so the batch is abandoned and no sixth
	// delay is ever scheduled (Req 7.6, 7.7). Asserting the count closes the
	// vacuous reading of this loop: a Shipper that scheduled a hundred backoffs
	// would satisfy every bound above.
	if !waitFor(func() bool { return s.Snapshot().AbandonedBatches == 1 }, p16WaitTimeout) {
		pbtFailf(t, seed, iteration, in, "the batch was not abandoned after %d attempts (Req 7.7): %+v",
			maxTransmitAttempts, s.Snapshot())
		return recorded
	}
	if got := clock.AfterCount(); got != maxTransmitAttempts-1 {
		pbtFailf(t, seed, iteration, in, "the clock recorded %d backoffs %v, want exactly %d (Req 7.6)",
			got, clock.AfterDurations(), maxTransmitAttempts-1)
	}
	if got := server.requests(); got != maxTransmitAttempts {
		pbtFailf(t, seed, iteration, in, "the endpoint received %d requests, want exactly %d (Req 7.6)",
			got, maxTransmitAttempts)
	}
	return recorded
}

// ---------------------------------------------------------------------------
// Coverage reporting
// ---------------------------------------------------------------------------

// p16Coverage is one generator-coverage claim: whether a case was reached, and
// what it was.
type p16Coverage struct {
	saw  bool
	what string
}

// p16AssertCoverage fails the test for every case the run never reached. A
// property whose boundaries were never generated passes while proving nothing,
// and with no shrinking there is nothing else to notice that from.
func p16AssertCoverage(t *testing.T, seed int64, cases []p16Coverage) {
	t.Helper()
	for _, c := range cases {
		if !c.saw {
			t.Errorf("the generator produced no iteration covering %s (seed %d)", c.what, seed)
		}
	}
}

// ---------------------------------------------------------------------------
// The property's own oracle tests
// ---------------------------------------------------------------------------

// TestP16OracleBaseRestatesRequirement74 pins the oracle against the
// requirement's table by hand, so a mistake in the re-derivation shows up here
// rather than as a mysteriously green property.
func TestP16OracleBaseRestatesRequirement74(t *testing.T) {
	schedule := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 1 * time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 5, want: 16 * time.Second},
		{attempt: 6, want: 30 * time.Second},
		{attempt: 7, want: 30 * time.Second},
		{attempt: 64, want: 30 * time.Second},
		{attempt: math.MaxInt, want: 30 * time.Second},
		{attempt: 0, want: 1 * time.Second},
		{attempt: -1, want: 1 * time.Second},
		{attempt: math.MinInt, want: 1 * time.Second},
	}
	for _, tc := range schedule {
		if got := p16OracleBase(tc.attempt, 0); got != tc.want {
			t.Errorf("p16OracleBase(%d, 0) = %v, want %v", tc.attempt, got, tc.want)
		}
	}

	clamps := []struct {
		retryAfter time.Duration
		want       time.Duration
	}{
		{retryAfter: 1 * time.Nanosecond, want: 1 * time.Second},
		{retryAfter: 999 * time.Millisecond, want: 1 * time.Second},
		{retryAfter: 1 * time.Second, want: 1 * time.Second},
		{retryAfter: 7 * time.Second, want: 7 * time.Second},
		{retryAfter: 300 * time.Second, want: 300 * time.Second},
		{retryAfter: 301 * time.Second, want: 300 * time.Second},
		{retryAfter: time.Duration(math.MaxInt64), want: 300 * time.Second},
	}
	for _, tc := range clamps {
		// The honoured value replaces the computed base whatever the attempt is.
		for _, attempt := range []int{1, 3, 6, 99} {
			if got := p16OracleBase(attempt, tc.retryAfter); got != tc.want {
				t.Errorf("p16OracleBase(%d, %v) = %v, want %v", attempt, tc.retryAfter, got, tc.want)
			}
		}
	}
	// A non-positive value is no header: the schedule applies.
	for _, retryAfter := range []time.Duration{0, -1, -time.Hour} {
		if got := p16OracleBase(3, retryAfter); got != 4*time.Second {
			t.Errorf("p16OracleBase(3, %v) = %v, want the schedule's %v", retryAfter, got, 4*time.Second)
		}
	}

	// The oracle must not be a restatement of the implementation, but for a
	// correct implementation the two must agree on every generated input. This is
	// the one place they are compared, and a divergence here is the honest signal
	// that one of them is wrong.
	r := rand.New(rand.NewSource(pbtDefaultSeed))
	for i := 0; i < pbtIterations; i++ {
		attempt := p16GenAttempt(r)
		retryAfter := p16GenRetryAfter(r)
		if got, want := baseRetryDelay(attempt, retryAfter), p16OracleBase(attempt, retryAfter); got != want {
			t.Fatalf("baseRetryDelay(%d, %v) = %v, but Req 7.4 reads as %v", attempt, retryAfter, got, want)
		}
	}
}

// TestP16HeaderRetryAfterHonoursOnly429DeltaSeconds pins the oracle's own header
// parsing, including the two cases that would otherwise make it disagree with the
// implementation for reasons unrelated to the property: a gigasecond value, whose
// naive conversion overflows int64, and a status other than 429.
func TestP16HeaderRetryAfterHonoursOnly429DeltaSeconds(t *testing.T) {
	honoured := []struct {
		header string
		want   time.Duration
	}{
		{header: "0", want: 0},
		{header: "1", want: 1 * time.Second},
		{header: "300", want: 300 * time.Second},
		{header: "301", want: 300 * time.Second},
		{header: "9999999999", want: 300 * time.Second},
		{header: "99999999999999999999999", want: 0}, // Atoi rejects it outright
		{header: "", want: 0},
		{header: "-5", want: 0},
		{header: "7.5", want: 0},
		{header: "Wed, 21 Oct 2015 07:28:00 GMT", want: 0},
	}
	for _, tc := range honoured {
		if got := p16HeaderRetryAfter(http.StatusTooManyRequests, tc.header); got != tc.want {
			t.Errorf("p16HeaderRetryAfter(429, %q) = %v, want %v", tc.header, got, tc.want)
		}
		// No status other than 429 honours a header at all (Req 7.4).
		for _, status := range []int{0, 202, 402, 500, 503, 599} {
			if got := p16HeaderRetryAfter(status, tc.header); got != 0 {
				t.Errorf("p16HeaderRetryAfter(%d, %q) = %v, want 0", status, tc.header, got)
			}
		}
	}

	// The oracle's parse must agree with the client's for every header the
	// delivery half generates, once both are read through the 429-only rule and
	// the clamp — otherwise the delivery half would be asserting against a value
	// the Shipper never saw.
	for _, header := range p16HeaderValues {
		want := p16OracleBase(1, p16HeaderRetryAfter(http.StatusTooManyRequests, header))
		got := p16OracleBase(1, retryAfterForStatus(http.StatusTooManyRequests, parseRetryAfter(header)))
		if got != want {
			t.Errorf("header %q: the client's parse yields base %v, the oracle's yields %v", header, got, want)
		}
	}
}

// TestP16ViolationDetectsOutOfBoundDelays proves each guard fires, so a green
// property run is evidence about the bounds rather than evidence that nothing is
// ever checked.
func TestP16ViolationDetectsOutOfBoundDelays(t *testing.T) {
	const base = 10 * time.Second

	within := []time.Duration{base, base + time.Nanosecond, base + time.Second, 12 * time.Second}
	for _, delay := range within {
		if got := p16Violation(delay, base); got != "" {
			t.Errorf("p16Violation(%v, %v) = %q, want no violation", delay, base, got)
		}
	}

	outside := []struct {
		name  string
		delay time.Duration
		base  time.Duration
	}{
		{name: "one nanosecond below the base", delay: base - time.Nanosecond, base: base},
		{name: "half the base", delay: base / 2, base: base},
		{name: "one nanosecond above 1.2 x base", delay: 12*time.Second + time.Nanosecond, base: base},
		{name: "twice the base", delay: 2 * base, base: base},
		{name: "above the 360 s ceiling", delay: p16DelayCeiling + time.Nanosecond, base: p16DelayCeiling},
	}
	for _, tc := range outside {
		t.Run(tc.name, func(t *testing.T) {
			if got := p16Violation(tc.delay, tc.base); got == "" {
				t.Errorf("p16Violation(%v, %v) = no violation, want one", tc.delay, tc.base)
			}
		})
	}

	// The floor claim.
	if got := p16FloorViolation(p16RetryAfterFloor); got != "" {
		t.Errorf("p16FloorViolation(%v) = %q, want no violation", p16RetryAfterFloor, got)
	}
	if got := p16FloorViolation(p16RetryAfterFloor - time.Nanosecond); got == "" {
		t.Error("p16FloorViolation just below the floor reported no violation")
	}

	// The 429-only check: it fires when a delay reached a clamped header value the
	// schedule could not explain, and stands down when the two are within jitter
	// of each other or the delay is below the header's value.
	const doubling = 1 * time.Second
	if got := p16HonouredHeaderViolation(503, 120*time.Second, doubling, 120*time.Second); got == "" {
		t.Error("a 503 whose delay reached its 120 s Retry-After was not reported (Req 7.4)")
	}
	if got := p16HonouredHeaderViolation(503, doubling, doubling, 120*time.Second); got != "" {
		t.Errorf("a 503 that followed the schedule was reported as honouring its header: %s", got)
	}
	if got := p16HonouredHeaderViolation(503, doubling, doubling, doubling); got != "" {
		t.Errorf("a header indistinguishable from the schedule was reported: %s", got)
	}
}

// TestP16GeneratorsProduceEveryBoundary pins the generators against the cases the
// two properties assert coverage of, over a wider loop than pbtIterations so it
// stays deterministic under a FUNDIS_PBT_SEED override.
func TestP16GeneratorsProduceEveryBoundary(t *testing.T) {
	const draws = 3000
	r, seed := pbtRand(t)

	var (
		attempts    = make(map[int]int)
		bases       = make(map[time.Duration]int)
		headers     = make(map[string]int)
		statuses    = make(map[int]int)
		hostile     int
		zeroJitter  int
		randJitter  int
		honoured429 int
		ignored5xx  int
	)
	for i := 0; i < draws; i++ {
		in := p16GenInput(r)
		attempts[in.Attempt]++
		statuses[in.Status]++
		honoured := retryAfterForStatus(in.Status, in.RetryAfter)
		bases[p16OracleBase(in.Attempt, honoured)]++
		if honoured > 0 && in.Status == http.StatusTooManyRequests {
			honoured429++
		}
		if in.Status != http.StatusTooManyRequests && in.RetryAfter > 0 {
			ignored5xx++
		}
		switch {
		case in.Jitter == "FixedJitter(0)":
			zeroJitter++
		case strings.HasPrefix(in.Jitter, "hostileJitter"):
			hostile++
		case strings.HasPrefix(in.Jitter, "RandJitter"):
			randJitter++
		}

		d := p16GenDelivery(r)
		headers[d.Header]++
	}

	for _, attempt := range p16AttemptBoundaries {
		if attempts[attempt] == 0 {
			t.Errorf("p16GenAttempt never produced the boundary attempt %d in %d draws (seed %d)", attempt, draws, seed)
		}
	}
	for _, base := range []time.Duration{1 * time.Second, 2 * time.Second, 30 * time.Second, 300 * time.Second} {
		if bases[base] == 0 {
			t.Errorf("no generated input produced the base %v in %d draws (seed %d)", base, draws, seed)
		}
	}
	for _, header := range []string{"", "0", "300", "1000", "-5"} {
		if headers[header] == 0 {
			t.Errorf("p16GenDelivery never produced the Retry-After value %q in %d draws (seed %d)", header, draws, seed)
		}
	}
	for _, counted := range []struct {
		n    int
		what string
	}{
		{honoured429, "a 429 with a usable Retry-After"},
		{ignored5xx, "a non-429 status carrying a Retry-After"},
		{hostile, "a hostile Jitter"},
		{zeroJitter, "FixedJitter(0)"},
		{randJitter, "RandJitter"},
		{statuses[0], "a transport failure, which has no status at all"},
	} {
		if counted.n == 0 {
			t.Errorf("the generators never produced %s in %d draws (seed %d)", counted.what, draws, seed)
		}
	}
}
