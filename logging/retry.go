package logging

// Retry policy: how long the Log_Shipper waits before retransmitting a
// Delivery_Batch, how many times it may transmit one, and what it does when
// those attempts run out (Req 7.2 through 7.7, 7.12).
//
// Everything here is either a pure function of (attempt, Retry-After) or a
// method on per-batch state that the delivery goroutine owns privately. Nothing
// in this file decides *whether* an outcome is retryable — that is the status
// classification of task 8.1 — and nothing here reads or writes a Shipper field
// other than the immutable clock, jitter, counters, and diagnostic logger. The
// split is what keeps three rules in one place:
//
//	the doubling schedule and the 30 s cap        (Req 7.4)
//	the Retry-After clamp to 1-300 s, 429 only    (Req 7.4)
//	the jitter that composes with both            (Req 7.5)
//
// Deliberately absent, because they belong to other tasks and other files:
//
//	the delivery loop's select over queue/ticker/flush/close -> task 6.5
//	HTTP status classification (what counts as retryable)    -> task 8.1
//	402 suspension and 403 permanent disable                 -> task 8.3
//	Flush, Close, and finalDrain's single-attempt drain       -> tasks 8.7, 8.8
//
// **Where the 429-only rule lives.** retryDelay honours a positive retryAfter
// unconditionally, exactly as the design's code does, because it takes the value
// as a parameter. The 429 restriction of Req 7.4 is applied one level up by
// retryAfterForStatus, which returns 0 for every status other than 429 — so a
// 5xx that happens to carry a Retry-After header still follows the doubling
// schedule. The delivery loop is expected to call
//
//	s.retryDelay(attempt, retryAfterForStatus(outcome.Status, outcome.RetryAfter))
//
// rather than passing outcome.RetryAfter straight through. Keeping the two
// separable is what lets the clamp be tested independently of the status table
// that task 8.1 owns.
//
// **Enqueue stays open throughout backoff (Req 7.12).** That is a property of
// what this file does *not* do: a backoff wait happens on the delivery
// goroutine, and enqueue neither consults retry state nor shares a lock with it,
// so a producer's non-blocking send into the Event_Queue succeeds during a
// backoff wait exactly as it does at idle. retryTimer hands the caller a channel
// instead of blocking inside this file, so the delivery loop can keep its queue,
// flush, and close arms live while a backoff is outstanding.

import (
	"log/slog"
	"math"
	"net/http"
	"time"
)

const (
	// maxTransmitAttempts is the per-Delivery_Batch transmission-attempt
	// ceiling: the events of one batch appear in at most 5 HTTP requests
	// (Req 7.6).
	maxTransmitAttempts = 5

	// baseRetryDelayUnit is the base delay before the first retransmission,
	// which every later base doubles from (Req 7.4).
	baseRetryDelayUnit = 1 * time.Second

	// maxBaseRetryDelay caps the doubling schedule (Req 7.4).
	maxBaseRetryDelay = 30 * time.Second

	// minRetryAfterDelay and maxRetryAfterDelay are the range a Retry-After
	// value is clamped into before it replaces the computed base (Req 7.4).
	minRetryAfterDelay = 1 * time.Second
	maxRetryAfterDelay = 300 * time.Second

	// maxJitterShare is the fraction of the base delay that jitter may add:
	// a uniform draw over 0 to 20 percent of the base, so every delay lies in
	// [base, 1.2 x base] (Req 7.5).
	maxJitterShare = 0.2

	// maxRetryDelay is the absolute ceiling on any single retry delay:
	// 300 s clamped Retry-After times 1.2 of jitter (Req 7.5). It is an
	// invariant of the arithmetic below rather than a value the schedule needs,
	// and retryDelay enforces it anyway so the bound holds even for a Jitter
	// implementation that violates its own [0, 1) contract.
	maxRetryDelay = 360 * time.Second

	// backoffCapAttempt is the first attempt number whose doubled base would
	// reach or exceed maxBaseRetryDelay: 1<<5 = 32 s. Every attempt at or above
	// it yields the cap.
	//
	// It exists to keep baseRetryDelay total. `1 << uint(attempt-1)` overflows
	// for a large attempt and wraps to a negative duration, which would read
	// back as "no delay" and turn a backoff into a tight retry loop. The attempt
	// counter is capped at maxTransmitAttempts so no caller in this package can
	// reach that, but a pure function should not depend on its caller's
	// discipline.
	backoffCapAttempt = 6
)

// retryVerdict is what to do with a Delivery_Batch whose latest transmission
// ended in a retryable status or transport failure.
type retryVerdict int

const (
	// retryVerdictRetry means retransmit after waiting the retry delay
	// (Req 7.2, 7.3).
	retryVerdictRetry retryVerdict = iota
	// retryVerdictAbandon means the attempt ceiling is reached: discard the
	// batch, count the drops, and write one ERROR (Req 7.7).
	retryVerdictAbandon
)

// batchAttempt is the per-Delivery_Batch retry state: how many times the batch
// has been transmitted, and what the most recent attempt reported.
//
// It is a local struct rather than a set of Shipper fields on purpose. The
// design's state-ownership table puts the attempt count and the backoff deadline
// in the delivery goroutine's private state, and tasks 6.5 and 8.3 are editing
// logging/shipper.go concurrently with this file. The delivery loop is expected
// to declare one of these next to its `pending` batch and reset it — `st =
// batchAttempt{}` — whenever it starts a new batch, which is what makes the
// count per-batch (Req 7.6).
//
// The zero value means "not yet transmitted", so attempts() reports 0 before the
// first send and 1 immediately after it.
type batchAttempt struct {
	// attempts is the number of transmissions performed for this batch. It
	// never exceeds maxTransmitAttempts.
	attempts int
	// status is the status of the most recent attempt, or 0 when that attempt
	// received no response.
	status int
	// err is the transport-level failure or elapsed timeout of the most recent
	// attempt, or nil when a response arrived.
	err error
}

// record accounts for one completed transmission of this batch and remembers
// what it reported.
//
// The increment saturates at maxTransmitAttempts rather than growing without
// bound. Req 7.6 states the count "never exceeds 5", and a saturating counter
// makes that true of the state itself, not merely of the loop that reads it: a
// caller bug that transmits a sixth time cannot produce an attempts value of 6,
// and exhausted() keeps reporting true.
//
// The last outcome is stored on every call, including the saturating one, so the
// abandonment diagnostic always reports the status or transport failure of the
// attempt that actually happened last (Req 7.7).
func (a *batchAttempt) record(outcome SendOutcome) {
	if a.attempts < maxTransmitAttempts {
		a.attempts++
	}
	a.status = outcome.Status
	a.err = outcome.Err
}

// exhausted reports whether this batch has used its 5 transmission attempts
// (Req 7.6).
func (a *batchAttempt) exhausted() bool { return a.attempts >= maxTransmitAttempts }

// verdict reports what to do after a transmission that classification found
// retryable.
//
// retryable is supplied by the caller because deciding it is task 8.1's status
// table, not this file's. verdict answers only the attempt-count half of
// Req 7.2, 7.3, and 7.7: retry while fewer than 5 attempts have happened,
// abandon once the fifth has ended retryably.
//
// A non-retryable outcome is not this function's business — the caller reaches a
// terminal state for it without consulting the attempt count — so it is reported
// as retryVerdictAbandon, the conservative answer, which cannot turn a
// misrouted call into an unbounded retry.
func (a *batchAttempt) verdict(retryable bool) retryVerdict {
	if !retryable || a.exhausted() {
		return retryVerdictAbandon
	}
	return retryVerdictRetry
}

// retryAfterForStatus returns the Retry-After value that may replace the
// computed base delay, which is the header's value for HTTP 429 and zero for
// every other status (Req 7.4).
//
// retryAfter arrives raw and unclamped from SendOutcome; parseRetryAfter in
// ingest.go has already rejected an absent, malformed, or negative header by
// yielding 0, so a positive value here is a non-negative integer count of
// seconds. retryDelay applies the 1-300 s clamp.
//
// The 429-only restriction is here rather than inside retryDelay so that the
// clamp and the doubling schedule remain testable without reference to a status,
// and so that the one place a status is interpreted for retry purposes is a
// single named function.
func retryAfterForStatus(status int, retryAfter time.Duration) time.Duration {
	if status != http.StatusTooManyRequests {
		return 0
	}
	return retryAfter
}

// baseRetryDelay computes the un-jittered base delay for the next
// retransmission of a batch that has been transmitted attempt times (Req 7.4).
//
//	attempt 1 -> 1 s, 2 -> 2 s, 3 -> 4 s, 4 -> 8 s, 5 -> 16 s, beyond -> 30 s
//
// A positive retryAfter replaces the computed base entirely, clamped to
// 1-300 s. The caller is responsible for passing 0 unless the response was a
// 429 (see retryAfterForStatus).
//
// It is a package-level function, not a method, because it depends on no
// Shipper state at all: given the same arguments it returns the same duration,
// which is what makes the schedule table assertable directly.
func baseRetryDelay(attempt int, retryAfter time.Duration) time.Duration {
	// An attempt below 1 can only come from a caller that computed a delay
	// before transmitting. The first retransmission's delay is the honest
	// answer, and it keeps the shift below well-defined.
	if attempt < 1 {
		attempt = 1
	}

	base := maxBaseRetryDelay
	if attempt < backoffCapAttempt {
		base = time.Duration(1<<uint(attempt-1)) * baseRetryDelayUnit
		if base > maxBaseRetryDelay {
			base = maxBaseRetryDelay // Req 7.4
		}
	}

	if retryAfter > 0 { // 429 only, per retryAfterForStatus
		base = clampDuration(retryAfter, minRetryAfterDelay, maxRetryAfterDelay) // Req 7.4
	}
	return base
}

// retryDelay is the delay to wait before the next retransmission: the base
// delay of Req 7.4 plus a jitter of up to 20 percent of it (Req 7.5).
//
// Every returned value lies in [base, 1.2 x base] and never exceeds
// maxRetryDelay. The final ceiling is belt-and-braces: with a Jitter that
// honours its [0, 1) contract the arithmetic cannot exceed 360 s, and the clamp
// makes the Req 7.5 bound hold regardless of the Jitter implementation supplied
// at construction.
//
// It is a method rather than a function only because the jitter source is
// injected per Shipper (Req 12.2). All of the policy is in baseRetryDelay.
func (s *Shipper) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	base := baseRetryDelay(attempt, retryAfter)
	delay := base + time.Duration(s.jitterFraction()*maxJitterShare*float64(base))
	if delay > maxRetryDelay {
		delay = maxRetryDelay // Req 7.5
	}
	if delay < base {
		// Unreachable for a fraction in [0, 1): the added term is non-negative.
		// Guarded so that a hostile Jitter cannot shorten a backoff below its
		// base, which is the direction that would matter — it would retry
		// sooner than Req 7.4 allows.
		delay = base
	}
	return delay
}

// retryTimer returns the channel that fires when the retry delay has elapsed,
// together with that delay.
//
// The wait goes through s.clock.After, so a test advances a fake clock rather
// than sleeping (Req 12.2), and the fake clock records the duration so the whole
// backoff schedule can be read back and compared against the table (property
// P16, task 8.5).
//
// A channel is returned rather than blocking here because the delivery loop must
// stay responsive during backoff: it selects over this channel alongside its
// close arm, so a Close during a 30 s backoff does not wait out the delay. The
// Event_Queue keeps accepting events either way, since enqueue never consults
// retry state (Req 7.12).
//
// The delay is returned as well so the caller can report it in a diagnostic
// without recomputing it — recomputing would draw a second jitter value and name
// a duration that was never waited.
func (s *Shipper) retryTimer(attempt int, retryAfter time.Duration) (<-chan time.Time, time.Duration) {
	delay := s.retryDelay(attempt, retryAfter)
	return s.retryClock().After(delay), delay
}

// waitRetry blocks until the retry delay has elapsed and reports how long it
// waited.
//
// It is the form for callers with nothing else to watch — the simple retry path
// and the tests of this file. A caller that must remain interruptible, which the
// delivery loop is, uses retryTimer and selects.
func (s *Shipper) waitRetry(attempt int, retryAfter time.Duration) time.Duration {
	timer, delay := s.retryTimer(attempt, retryAfter)
	<-timer
	return delay
}

// abandonBatch discards a Delivery_Batch that ended its fifth transmission
// attempt retryably (Req 7.7).
//
// It performs every effect Req 7.7 enumerates:
//
//   - releases the events from the in-flight count,
//   - drops the batch's events under reasonAbandonedBatch,
//   - increments the abandoned-batch counter,
//   - writes exactly one ERROR Self_Diagnostic naming the discarded count and
//     the last observed status or transport failure category.
//
// The in-flight release lives here rather than in the caller because the
// abandonment path is the one that reaches a terminal state for these events; a
// decrement in both places would double-count. transmit relies on that and
// deliberately performs no release of its own on this path.
//
// It comes **first**, which is the ordering counters_test.go and transmit both
// document: releasing before recording the drop returns the events to the derived
// queue depth for one instant, whereas recording the drop first would count them
// as dropped *and* in flight at the same time and break the Req 11.4 identity for
// any Snapshot taken in between — and a Snapshot can be taken at any instant, by
// a health endpoint, from another goroutine.
//
// No Source_Token can reach the diagnostic: the only strings written are a
// category, a status number, and the endpoint rendered by diagEndpoint, which
// strips userinfo and the query string (Req 7.13, 10.4, 10.5). The transport
// error text comes from IngestClient.wrapSendError, which has already removed
// the *url.Error wrapper for the same reason.
//
// It is safe to call with an empty batch: the counters absorb a zero increment,
// and the diagnostic still reports that a batch was abandoned, which is the
// honest account of a caller that reached the attempt ceiling.
func (s *Shipper) abandonBatch(batch []*Event, a batchAttempt) {
	if s == nil {
		return
	}
	n := len(batch)
	if s.counters != nil {
		s.counters.addInFlight(-int64(n))                      // release first: see above
		s.counters.addDropped(reasonAbandonedBatch, uint64(n)) // Req 7.7
		s.counters.addAbandonedBatch()
	}

	args := []any{
		slog.Int("discarded_events", n),
		slog.Int("attempts", a.attempts),
		slog.String("endpoint", s.diagEndpoint()),
	}
	if a.status > 0 {
		args = append(args, slog.Int("status", a.status))
	} else {
		args = append(args, slog.String("failure", retryTransportCategory(a.err)))
	}
	if a.err != nil {
		args = append(args, slog.String("error", a.err.Error()))
	}
	s.diagf(slog.LevelError, "betterstack ingest batch abandoned after exhausting retry attempts", args...)
}

// retryTransportCategory names the transport failure category Req 7.7 requires
// the abandonment diagnostic to report when the last attempt received no
// response.
//
// The three categories are the three distinguishable cases of a SendOutcome with
// Status 0: an elapsed deadline, some other transport-level failure, and — only
// reachable from a caller bug — no failure at all. isTimeout in ingest.go is
// what separates the first two, checking both the context deadline of Req 9.6
// and the http.Client timeout of Req 5.10.
//
// The name is scoped to this file because task 8.1 owns the status side of
// classification; this function exists only to label the one diagnostic above.
func retryTransportCategory(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case isTimeout(err):
		return "timeout"
	default:
		return "transport"
	}
}

// jitterFraction reads one jitter value from the injected source, sanitised into
// [0, 1).
//
// The sanitising matters because the fraction is multiplied by a duration: a NaN
// would make the product NaN, whose conversion to time.Duration is
// implementation-defined and can yield an enormous or negative delay, and a
// fraction above 1 would break the 1.2 x base bound of Req 7.5. FixedJitter and
// RandJitter both honour the contract; this guard covers a Jitter supplied from
// outside the package.
//
// A nil jitter yields 0, which is a delay of exactly the base — still inside
// [base, 1.2 x base]. Only a Shipper composed literally rather than through
// NewShipper can reach it, since applyDefaults always fills the field.
func (s *Shipper) jitterFraction() float64 {
	if s == nil || s.jitter == nil {
		return 0
	}
	f := s.jitter.Fraction()
	switch {
	case math.IsNaN(f), f < 0:
		return 0
	case f > maxJitterFraction:
		return maxJitterFraction
	default:
		return f
	}
}

// retryClock returns the Clock every backoff wait is measured on (Req 12.2).
//
// The fallback covers a Shipper composed literally, whose clock field is nil;
// NewShipper always sets it through applyDefaults. It mirrors diagNow in
// logging/diag.go, which makes the same allowance for the same reason.
func (s *Shipper) retryClock() Clock {
	if s != nil && s.clock != nil {
		return s.clock
	}
	return SystemClock()
}

// clampDuration returns d confined to [lo, hi].
//
// lo wins when the range is inverted, so an inverted range yields a value inside
// no meaningful interval but never an unbounded one. Every call site in this
// package passes a well-ordered pair.
func clampDuration(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}
