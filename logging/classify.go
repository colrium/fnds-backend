package logging

// HTTP status classification: the one place that decides what a transmission
// attempt *meant*.
//
// classify is a pure function of a SendOutcome. It performs none of the actions
// its verdict calls for — it increments no counter, writes no Self_Diagnostic,
// changes no state, and waits for nothing. Those belong to the components that
// own the corresponding state:
//
//	retry, backoff, the Retry-After clamp, abandonment -> task 8.2 (logging/retry.go)
//	402 suspension and 403 permanent disable           -> task 8.3
//	the delivery loop that applies a verdict           -> task 6.5 (run)
//
// Keeping it pure buys three things. It makes the whole of Req 7's status table
// readable in one screen, with no counter or clock interleaved. It makes the
// classification of statuses that no HTTP server can actually return testable at
// all: net/http treats 1xx as informational and will not deliver 199 to a client
// as a final status, and 600 is not transmittable, yet Req 7.15 covers both, so
// the only way to check them is to call the classifier directly. And it is what
// lets Property 11 (task 8.4) compare this function against an oracle derived
// from the requirement's ranges over arbitrary statuses.
//
// Req 7.16 constrains *when* a verdict may be acted on, not what it is: the
// response body has already been read-capped and closed inside
// IngestClient.Send before any SendOutcome exists, so every caller of this
// function is necessarily past that point. There is nothing to enforce here —
// the guarantee is structural, and the comment in Send is where it is made.

import (
	"log/slog"
	"net/http"
)

// deliveryOutcome is what the delivery loop must do with the Delivery_Batch it
// just transmitted.
//
// It captures the state-machine edge, not the diagnostic: a 202 and a 200 are
// the same outcome — batch delivered, no retransmission, delivered counter
// advances — and differ only in that the second one also warns (Req 7.1, 7.14).
// The diagnostic travels in classification.diag, which is why no outcome here
// exists solely to carry a message.
type deliveryOutcome int

const (
	// outcomeNoRequest means no request was made and nothing happened: the
	// zero SendOutcome that Send returns for an empty batch (Req 5.12). It is
	// emphatically *not* a transport failure, so it must not be retried and must
	// advance no counter. The distinction matters because both cases carry
	// Status == 0 and are told apart only by Err.
	outcomeNoRequest deliveryOutcome = iota

	// outcomeDelivered means the Ingest_Endpoint accepted the Delivery_Batch:
	// discard it without retransmission and advance the delivered-event counter
	// by the number of Log_Events it carried (Req 7.1, 7.14).
	outcomeDelivered

	// outcomeRetry means the attempt failed in a way that may succeed later:
	// HTTP 429, any 5xx, a transport-level failure, or an elapsed timeout
	// (Req 7.2, 7.3). The batch is eligible for retransmission; whether it is
	// actually retransmitted depends on the attempt count, which task 8.2 owns.
	// No per-attempt diagnostic accompanies it — Req 7.7's ERROR is written once
	// on abandonment, not once per attempt.
	outcomeRetry

	// outcomeDrop means the response is non-retryable: discard the
	// Delivery_Batch without retransmission and advance the dropped-event
	// counter (Req 7.11, 7.15). Only the batch is lost; the Event_Queue and the
	// Shipper's state are untouched.
	outcomeDrop

	// outcomeSuspend is HTTP 402: drop the batch as non-retryable, then suspend
	// transmission for 15 minutes, resuming at the first flush boundary after
	// the window (Req 7.10). Enqueue stays open throughout (Req 7.12).
	outcomeSuspend

	// outcomeDisable is HTTP 403: drop the batch, discard every Log_Event still
	// held in the Event_Queue and count those drops too, and enter disabled
	// state for the remaining lifetime of the process, transmitting nothing
	// thereafter — including at flush boundaries and during Close (Req 7.8,
	// 7.9).
	outcomeDisable
)

// String names an outcome for test failure messages and for reading a verdict in
// a debugger. No Self_Diagnostic uses it: the operator-facing text is chosen by
// the diagRequirement, whose wording each requirement fixes.
func (o deliveryOutcome) String() string {
	switch o {
	case outcomeNoRequest:
		return "no-request"
	case outcomeDelivered:
		return "delivered"
	case outcomeRetry:
		return "retry"
	case outcomeDrop:
		return "drop"
	case outcomeSuspend:
		return "suspend"
	case outcomeDisable:
		return "disable"
	default:
		return "unknown"
	}
}

// diagRequirement is the single Self_Diagnostic an outcome calls for, if any.
//
// Level and required content are one field rather than a level plus a set of
// booleans, because Req 7 fixes them together: the WARN of Req 7.14 must contain
// the status, the ERROR of Req 7.11 must contain the status *and* the discarded
// count, the ERROR of Req 7.15 must contain the status, and the ERRORs of
// Req 7.10 and 7.8 are identified by what they say — account or quota rejection,
// and Source_Token rejected — rather than by a status. Encoding the pair as one
// value makes every illegal combination unrepresentable, and leaves the caller a
// single value to switch on when it composes the message.
//
// No diagnostic named here may contain the Source_Token (Req 7.13, 10.4), and
// any that names the endpoint renders scheme, host, and path only (Req 10.5) via
// Shipper.diagEndpoint. Neither is enforceable in this file, which produces no
// text at all; both are properties of diag.go, where the text is written.
type diagRequirement int

const (
	// diagNone writes nothing. It covers a delivered 202 (Req 7.1), every
	// retryable attempt (Req 7.2, 7.3), and the empty-batch no-op (Req 5.12).
	diagNone diagRequirement = iota

	// diagWarnStatus is one WARN containing the response status, for a 2xx other
	// than 202 (Req 7.14). The batch was still delivered; the warning exists
	// because an ingest endpoint answering 200 instead of 202 is answering
	// something other than what Better Stack documents.
	diagWarnStatus

	// diagErrorStatusCount is one ERROR containing the response status and the
	// number of discarded Log_Events, for 406 and 413 (Req 7.11).
	diagErrorStatusCount

	// diagErrorStatus is one ERROR containing the response status, for every
	// other non-retryable status (Req 7.15).
	diagErrorStatus

	// diagErrorQuota is one ERROR stating that Better Stack rejected the
	// Delivery_Batch for account or quota reasons, for 402 (Req 7.10).
	diagErrorQuota

	// diagErrorToken is one ERROR stating that the Source_Token was rejected,
	// for 403 (Req 7.8). It says the token was rejected; it never says what the
	// token is (Req 7.13).
	diagErrorToken
)

// level reports the slog level at which the diagnostic is written.
//
// diagNone has no level, and callers must not reach this method for it — they
// check diagRequirement != diagNone first. ERROR is returned for it anyway
// rather than panicking, because a bug that loses a diagnostic is preferable to
// one that crashes the delivery goroutine, and ERROR is the level that would
// make such a bug visible.
func (d diagRequirement) level() slog.Level {
	if d == diagWarnStatus {
		return slog.LevelWarn
	}
	return slog.LevelError
}

// namesStatus reports whether the diagnostic's text must contain the HTTP
// response status.
//
// True for the three requirements that say so — 7.11, 7.14, 7.15 — and false for
// the 402 and 403 ERRORs, which Req 7.10 and 7.8 specify by meaning instead. A
// caller composing the message may of course include the status anyway; this
// reports what is *required*, which is what a test can assert against.
func (d diagRequirement) namesStatus() bool {
	switch d {
	case diagWarnStatus, diagErrorStatusCount, diagErrorStatus:
		return true
	default:
		return false
	}
}

// namesCount reports whether the diagnostic's text must contain the number of
// discarded Log_Events. Only Req 7.11 requires it.
func (d diagRequirement) namesCount() bool {
	return d == diagErrorStatusCount
}

// classification is the complete meaning of one transmission attempt: every
// distinction the delivery loop needs in order to act, and nothing else.
//
// The two fields are independent axes. outcome is the state-machine edge and the
// counter effect; diag is the operator-facing consequence. Splitting them is
// what keeps "delivered" and "delivered but the endpoint answered 200" from
// being two outcomes whose counter and retry behaviour a reader has to compare
// line by line to discover are identical.
//
// The counter effect is derived from outcome by retriesBatch, creditsDelivered,
// creditsDropped, and dropReason rather than stored, so no classification can
// exist that claims, for example, both a delivery and a drop of the same batch.
type classification struct {
	// outcome is what the delivery loop must do with the batch.
	outcome deliveryOutcome
	// diag is the single Self_Diagnostic this outcome calls for, or diagNone.
	diag diagRequirement
}

// retriesBatch reports whether the Delivery_Batch is eligible for
// retransmission (Req 7.2, 7.3).
//
// Eligible, not scheduled: the attempt count decides whether a retryable outcome
// becomes a retransmission or an abandonment, and that counting lives in
// task 8.2 with the backoff schedule it feeds. This function knows nothing about
// attempts, which is why it can be evaluated from a status alone.
func (c classification) retriesBatch() bool {
	return c.outcome == outcomeRetry
}

// creditsDelivered reports whether the delivered-event counter advances by the
// number of Log_Events in the batch (Req 7.1, 7.14).
func (c classification) creditsDelivered() bool {
	return c.outcome == outcomeDelivered
}

// creditsDropped reports whether the dropped-event counter advances by the
// number of Log_Events in the batch (Req 7.10, 7.11, 7.15).
//
// It is false for outcomeRetry: a retryable attempt has not reached a terminal
// state, so its events are still in flight, and counting them as dropped here
// and as delivered after a successful retry would double-count against the
// Req 11.4 accounting identity. The drop that follows an exhausted fifth attempt
// is Req 7.7's, is attributed to reasonAbandonedBatch, and belongs to task 8.2.
//
// For outcomeDisable it accounts for the batch only. The additional drops for
// the Log_Events drained out of the Event_Queue are also required by Req 7.8,
// but their number is not knowable from a status — it depends on what the queue
// holds at that instant — so task 8.3 counts them as it drains.
func (c classification) creditsDropped() bool {
	switch c.outcome {
	case outcomeDrop, outcomeSuspend, outcomeDisable:
		return true
	default:
		return false
	}
}

// dropReason is the DropReason to attribute the drop to, and is meaningful only
// when creditsDropped reports true.
//
// Every drop this file can produce is reasonNonRetryable: the response said, in
// one way or another, that retransmitting these Log_Events cannot help
// (Req 7.8, 7.10, 7.11, 7.15). reasonAbandonedBatch is deliberately not
// reachable from here — it means "retryable to the last attempt", which is a
// fact about the attempt count and not about any single response (Req 7.7).
func (c classification) dropReason() DropReason {
	return reasonNonRetryable
}

// classify decides what one transmission attempt meant. It is a pure function:
// same SendOutcome in, same classification out, with no observable effect.
//
// The input contract is IngestClient.Send's, and all three of its cases are
// distinguished here:
//
//   - Status > 0 with a nil Err: a response arrived, and the status table below
//     decides everything.
//   - Status == 0 with a non-nil Err: no response arrived. A transport-level
//     failure and an elapsed timeout are both retryable and are deliberately not
//     told apart (Req 7.3); isTimeout separates them only to sharpen the failure
//     category in Req 7.7's abandonment diagnostic, which task 8.2 writes.
//   - Status == 0 with a nil Err: the empty-batch no-op, which opened no
//     connection (Req 5.12). Reading it as a transport failure would retry a
//     batch that does not exist, so it is its own outcome.
//
// The single Status <= 0 guard covers the second and third cases together, and
// with them a negative status that no net/http response can carry but a
// hand-built SendOutcome can.
//
// Order matters inside the switch, and only in one place: 402, 403, 406, 413,
// and 429 must be matched before the 5xx range and before the default, which is
// automatic here because each is an equality case listed ahead of the ranges.
// Every arm cites the criterion it implements, and the arms are in the order of
// the design's classification table so the two can be read side by side.
func classify(o SendOutcome) classification {
	if o.Status <= 0 {
		if o.Err == nil {
			return classification{outcome: outcomeNoRequest} // Req 5.12: empty batch, no request
		}
		return classification{outcome: outcomeRetry} // Req 7.3: transport failure or timeout
	}

	switch {
	case o.Status == http.StatusAccepted: // 202
		// Req 7.1: discard without retransmission, delivered += n, no diagnostic.
		return classification{outcome: outcomeDelivered}

	case o.Status >= 200 && o.Status <= 299:
		// Req 7.14: still delivered, plus exactly one WARN naming the status.
		return classification{outcome: outcomeDelivered, diag: diagWarnStatus}

	case o.Status == http.StatusPaymentRequired: // 402
		// Req 7.10: drop the batch, then suspend transmission for 15 minutes.
		return classification{outcome: outcomeSuspend, diag: diagErrorQuota}

	case o.Status == http.StatusForbidden: // 403
		// Req 7.8, 7.9: drop the batch, drain the queue, disable permanently.
		return classification{outcome: outcomeDisable, diag: diagErrorToken}

	case o.Status == http.StatusNotAcceptable, // 406
		o.Status == http.StatusRequestEntityTooLarge: // 413
		// Req 7.11: non-retryable, ERROR naming the status and the count.
		return classification{outcome: outcomeDrop, diag: diagErrorStatusCount}

	case o.Status == http.StatusTooManyRequests: // 429
		// Req 7.2, and the only status whose Retry-After header is honoured —
		// which retryDelay reads from the SendOutcome, not from here (Req 7.4).
		return classification{outcome: outcomeRetry}

	case o.Status >= 500 && o.Status <= 599:
		// Req 7.2: the server failed; the same events may be accepted later.
		return classification{outcome: outcomeRetry}

	default:
		// Req 7.15: every remaining status — 1xx, 3xx, the other 4xx, and
		// anything at or above 600 — is non-retryable with an ERROR naming the
		// status. net/http never surfaces a 1xx as a final status and cannot
		// carry 600 at all, so this arm is reachable in production only for 3xx
		// and the other 4xx; it is written to cover the whole complement anyway,
		// because Req 7.15 is written that way and a classifier with a hole in
		// it would fail open into a retry loop.
		return classification{outcome: outcomeDrop, diag: diagErrorStatus}
	}
}
