package logging

// The periodic health summary: the one Self_Diagnostic that reports how log
// shipping is actually going, every 5 minutes, and only when something happened
// (Req 11.6, 11.7).
//
// Three rules shape everything in this file.
//
//  1. **One snapshot per summary.** Every number in the diagnostic comes from a
//     single Snapshot call, so the counters an operator reads are mutually
//     consistent and satisfy the Req 11.4 accounting identity. Reading the
//     counters field by field would let a delivery land between two reads and
//     produce a line whose enqueued count does not balance — the one thing that
//     would make an operator distrust the summary entirely.
//
//  2. **Silence is a requirement, not an optimisation.** Req 11.7 says an
//     interval in which zero counters increased produces zero diagnostics and
//     zero HTTP requests. That is why the previous snapshot is remembered: the
//     only way to know nothing happened is to compare. Zero HTTP requests is
//     free here — this file never touches the ingest client, and a summary is not
//     an event.
//
//  3. **A summary can never become a Log_Event.** It is written through
//     s.diagf, whose logger is bound straight to the diagnostic writer and is
//     not reachable from the fan-out handler, so Req 11.8 holds structurally
//     rather than by a runtime check — the same guarantee that covers the startup
//     summary (Req 8.1, 8.2). Nothing in this file enqueues, and nothing in it
//     reads cfg.SourceToken.
//
// Where the interval comes from. The evaluation is driven by its own Ticker on
// the injected Clock (Req 12.2), created and stopped by the delivery goroutine.
// Piggybacking on the existing flush ticker would have been a smaller edit to
// run, and it is wrong: the flush interval is configurable up to 300 s (Req 12.1),
// so a 290 s flush interval would evaluate at 290 s and 580 s and miss the 300 s
// boundary by 280 s, while Req 11.6 allows 5 seconds. A dedicated 5-minute ticker
// makes the bound independent of the flush interval.

import (
	"log/slog"
	"time"
)

const (
	// healthSummaryInterval is the evaluation period of Req 11.6, measured on
	// the injected clock alone, so a test drives a whole interval by advancing a
	// fake clock rather than by waiting 5 minutes.
	healthSummaryInterval = 5 * time.Minute

	// healthSummaryMessage is the operator-facing text. It says what the line is
	// and nothing about the endpoint: the summary reports the Log_Shipper's own
	// counters, and the endpoint already appears in the startup summary and in
	// every delivery-failure diagnostic.
	healthSummaryMessage = "log shipper health summary"
)

// healthDropReasonKeys names the per-reason dropped-event counters Req 11.6
// requires the summary to carry, indexed by DropReason.
//
// It is an array sized by numDropReasons rather than a switch, so adding a sixth
// reason would produce a compile-time array-length mismatch here instead of a
// summary that silently omits it. Req 11.5 fixes the count at five, and the five
// keys below are the five reasons.
var healthDropReasonKeys = [numDropReasons]string{
	reasonQueueFull:       "dropped_queue_full",
	reasonAbandonedBatch:  "dropped_abandoned_batch",
	reasonNonRetryable:    "dropped_non_retryable",
	reasonConversionPanic: "dropped_conversion_panic",
	reasonAfterClose:      "dropped_after_close",
}

// healthMonitor is the periodic-summary state: the ticker that paces evaluations
// and the snapshot the next evaluation compares against.
//
// It carries no mutex and no atomic, and that is deliberate. It is created,
// ticked, read, and written by the delivery goroutine alone — run creates it,
// run's health arm is the only caller of maybeHealthSummary, and run stops it on
// the way out — so it is in the same category as the pending batch: state private
// to one goroutine, which needs no synchronisation and must never be touched from
// another. Making it a Shipper field guarded by a lock would add the package's
// second lock to serve exactly one reader.
//
// prev starts as the zero Snapshot, which is the honest baseline: at
// construction the Shipper has counted nothing, so the first interval reports
// whatever happened during it and stays silent if that was nothing.
type healthMonitor struct {
	ticker Ticker
	prev   Snapshot
}

// newHealthMonitor starts the 5-minute evaluation ticker on the injected clock
// (Req 11.6, 12.2).
//
// It is called from run, so a disabled Shipper gets no monitor and no ticker at
// all: Start returns before run for a Shipper disabled at startup (Req 1.11),
// which is one half of Req 11.10's zero periodic summaries. The other half —
// a Shipper that disables mid-life, after HTTP 403 or five delivery panics, whose
// run loop keeps looping — is handled in maybeHealthSummary, because the ticker
// created here goes on ticking through that transition.
//
// A nil Shipper yields a monitor with no ticker rather than a nil monitor, so
// every method below has a receiver and run's select arm has a channel to name.
func (s *Shipper) newHealthMonitor() *healthMonitor {
	if s == nil {
		return &healthMonitor{}
	}
	return &healthMonitor{ticker: s.deliveryClock().NewTicker(healthSummaryInterval)}
}

// tick returns the channel run's health arm receives from.
//
// A monitor with no ticker returns nil, and a receive from a nil channel blocks
// forever — which is exactly the right behaviour in a select: the arm is present
// but can never fire, so a Shipper composed literally with no clock runs its
// delivery loop with no summaries instead of panicking on a nil ticker.
func (m *healthMonitor) tick() <-chan time.Time {
	if m == nil || m.ticker == nil {
		return nil
	}
	return m.ticker.C()
}

// stop releases the evaluation ticker. It is idempotent and safe on a monitor
// that never had one, so run can defer it unconditionally.
func (m *healthMonitor) stop() {
	if m == nil || m.ticker == nil {
		return
	}
	m.ticker.Stop()
}

// maybeHealthSummary performs one periodic evaluation: it writes exactly one INFO
// Self_Diagnostic when at least one counter increased since the previous
// evaluation, and writes nothing at all when none did (Req 11.6, 11.7).
//
// The whole evaluation reads the counters **once**. snap is the single consistent
// observation Req 11.4 guarantees, it is what the comparison uses, and it is what
// every field of the diagnostic is rendered from — so the queue depth in the line
// balances against the enqueued and delivered counts in the same line. Taking a
// second snapshot for the write would reintroduce exactly the inconsistency
// Snapshot exists to prevent.
//
// **Why gauges are excluded from the change test.** Req 11.7 keys silence on
// counters, and Req 11.1 defines the counters as the six monotonic totals, with
// Req 11.5 adding the five per-reason ones. QueueDepth and InFlight are neither:
// Req 11.3 introduces them as current values, and they fall as well as rise, so
// "increased" is not a property of them. The case this decides is real — an event
// enqueued in one interval and picked up by the delivery goroutine in the next
// moves InFlight from 0 to 1 and QueueDepth from 1 to 0 with no counter touched —
// and Req 11.7 requires silence for it, not a summary. Nothing is lost: the
// delivery or drop that event is on its way to increments a counter, and the next
// evaluation reports it along with the gauges as they then stand.
//
// **Zero HTTP requests**, the second half of Req 11.7, is structural: this
// function calls no ingest client, enqueues nothing, and touches no batching
// state. A silent evaluation performs one Snapshot and returns.
//
// It writes nothing while disabled (Req 11.10). A mid-life disable — HTTP 403
// (Req 7.8) or five recovered delivery panics (Req 8.4) — leaves run looping and
// this ticker ticking, so the state check is what makes the summaries stop, and
// stop permanently, since disabled is for the remaining lifetime of the process.
// A *suspended* Shipper is deliberately not excluded: Req 11.10 names the
// disabled state only, and a 15-minute quota suspension is precisely when an
// operator most wants to see the queue depth climbing.
//
// prev is left untouched on the disabled path. That is not laziness: disabled is
// terminal, so no later evaluation can consult the baseline, and leaving it alone
// keeps the "baseline advances at each evaluation that could report" rule single.
func (s *Shipper) maybeHealthSummary(m *healthMonitor) {
	if s == nil || m == nil {
		return
	}
	if s.state.Load() == stateDisabled {
		return // Req 11.10: zero periodic summaries while disabled
	}

	snap := s.Snapshot() // the one observation every number below comes from

	if !healthCountersIncreased(m.prev, snap) {
		// Req 11.7: zero Self_Diagnostics and zero HTTP requests for an interval
		// in which nothing was counted. The baseline is not advanced because it
		// cannot have moved — snap equals it on every counter — so there is
		// nothing to advance to.
		return
	}
	// The baseline advances before the write, so the interval boundary that
	// produced this summary is the one the next comparison starts from. Counters
	// that increment while the diagnostic is being written belong to the next
	// interval and will be reported there.
	m.prev = snap

	s.diagf(slog.LevelInfo, healthSummaryMessage, healthSummaryArgs(snap)...)
}

// healthSummaryArgs renders one Snapshot as the attribute list of the summary:
// every counter, every per-reason dropped-event count, the Event_Queue depth, and
// the in-flight count (Req 11.6).
//
// It takes a Snapshot by value and reads nothing else, which is what makes the
// "one snapshot" claim checkable by reading this function: there is no Shipper in
// scope for it to sample a second time, and no configuration for it to leak. In
// particular no Source_Token can appear in a summary (Req 7.13, 10.4) — every
// value written here is an unsigned integer.
func healthSummaryArgs(snap Snapshot) []any {
	args := make([]any, 0, 6+numDropReasons+2)
	args = append(args,
		// The six counters of Req 11.1.
		slog.Uint64("enqueued", snap.Enqueued),
		slog.Uint64("delivered", snap.Delivered),
		slog.Uint64("dropped", snap.Dropped),
		slog.Uint64("conversion_panics", snap.ConversionPanics),
		slog.Uint64("retried_batches", snap.RetriedBatches),
		slog.Uint64("abandoned_batches", snap.AbandonedBatches),
	)
	// All five per-reason counters of Req 11.5, always, including the zeros: an
	// operator reading the line needs to see that no event was dropped after
	// close, and an omitted key is indistinguishable from a forgotten one.
	for reason, key := range healthDropReasonKeys {
		args = append(args, slog.Uint64(key, snap.DroppedByReason[reason]))
	}
	// The two gauges of Req 11.3.
	return append(args,
		slog.Uint64("queue_depth", snap.QueueDepth),
		slog.Uint64("in_flight", snap.InFlight),
	)
}

// healthCountersIncreased reports whether at least one counter grew between two
// snapshots, which is the condition Req 11.6 writes a summary on and Req 11.7
// stays silent on.
//
// It compares the eleven cumulative counters — the six of Req 11.1 and the five
// per-reason ones of Req 11.5 — and no gauge. See maybeHealthSummary for why
// QueueDepth and InFlight are excluded.
//
// The comparison is strictly greater-than rather than not-equal. Counters never
// decrease for the lifetime of the process (Req 11.1), so on every reachable pair
// the two are the same test; using > means a decrease, which could only come from
// a bug elsewhere, produces silence rather than a summary claiming an increase
// that did not happen.
func healthCountersIncreased(prev, cur Snapshot) bool {
	if cur.Enqueued > prev.Enqueued ||
		cur.Delivered > prev.Delivered ||
		cur.Dropped > prev.Dropped ||
		cur.ConversionPanics > prev.ConversionPanics ||
		cur.RetriedBatches > prev.RetriedBatches ||
		cur.AbandonedBatches > prev.AbandonedBatches {
		return true
	}
	for reason := range cur.DroppedByReason {
		if cur.DroppedByReason[reason] > prev.DroppedByReason[reason] {
			return true
		}
	}
	return false
}
