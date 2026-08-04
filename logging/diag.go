package logging

// The Self_Diagnostic path: everything the Log_Shipper says about its own
// operation.
//
// One structural rule governs this whole file, and it is the reason the file
// exists at all: **a Self_Diagnostic is written through a logger bound directly
// to a writer, never through slog.Default()** (Req 8.1). The Better_Stack_Handler
// is not reachable from s.diag, so a diagnostic describing a failed delivery
// cannot itself become a Log_Event and cannot produce another delivery attempt
// (Req 8.2). That guarantee is a property of the wiring rather than of a runtime
// check, which is what makes it hold for every present and future diagnostic,
// including the startup summary (Req 11.8).
//
// Reconciling Req 8.1 with Req 12.8. Req 8.1 says diagnostics go through the
// Console_Handler, which writes to stdout (Req 2.5); Req 12.8 says the writer
// defaults to standard error when none was supplied. Config.DiagWriter is the
// single source of truth for both: main.go sets it to os.Stdout, so in production
// the diagnostic logger *is* console-shaped and stdout-bound, satisfying Req 8.1,
// and applyDefaults falls back to os.Stderr only for a Shipper built without that
// wiring — the isolated case Req 12.8 addresses. Tests inject a *bytes.Buffer and
// read the diagnostics back out of memory.
//
// The Source_Token appears in no diagnostic written from here, ever (Req 7.13,
// 10.4). Nothing in this file reads cfg.SourceToken, and the only endpoint
// rendering available to it strips userinfo and the query string (Req 10.5).
//
// Deliberately absent, because they belong to later tasks:
//
//	the ingest-failure and abandonment diagnostics -> tasks 8.1 through 8.3
//	the 5-panic permanent-disable diagnostic       -> task 8.6
//	the close-budget-expired diagnostic            -> task 8.8
//
// The periodic 5-minute health summary now lives in logging/health.go, and it is
// one more diagf caller: that is the whole of why Req 11.8 covers it.
//
// Each of those is a diagf call with different arguments. The mechanism they all
// share is here.

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	// maxDiagPanicChars caps a recovered panic value at its first 1024
	// characters — characters, not bytes, so a multi-byte panic text is cut on a
	// rune boundary (Req 2.6, 8.3).
	maxDiagPanicChars = 1024
	// maxDiagStackBytes caps a captured stack at its first 8192 bytes
	// (Req 2.6, 8.3).
	maxDiagStackBytes = 8192

	// dropDiagWindow is the minimum interval between two queue-full drop
	// diagnostics, measured on the injected clock (Req 6.4, 6.8).
	dropDiagWindow = 60 * time.Second

	// dropDiagNever is the stored timestamp meaning "no drop diagnostic has been
	// written since process start" (Req 6.4). It is the zero value on purpose,
	// so a Shipper composed literally starts out allowed to write.
	dropDiagNever int64 = 0

	// unknownIngestHost stands in for an endpoint whose host cannot be
	// determined. See ingestHost.
	unknownIngestHost = "unknown"
)

// newDiagLogger builds the Self_Diagnostic logger: a JSON handler bound straight
// to cfg.DiagWriter, tagged with the logshipper marker attribute (Req 8.1).
//
// Bound to a writer rather than to slog.Default() is the whole point, and it is
// what makes Req 8.2 structural rather than defensive: there is no path from this
// logger to the FanoutHandler, to the Better_Stack_Handler, or to the
// Event_Queue, so no diagnostic can be converted into a Log_Event no matter what
// level it carries.
//
// The handler is given a nil HandlerOptions, so it emits at every level with no
// level gate of its own: a Self_Diagnostic is not subject to the
// Shipping_Threshold, and an INFO summary must appear even when the threshold is
// ERROR (Req 11.9).
func newDiagLogger(cfg Config) *slog.Logger {
	w := cfg.DiagWriter
	if w == nil {
		// Req 12.8: standard error when no writer was supplied. applyDefaults
		// already does this for every Config that reaches NewShipper; the
		// fallback survives so this helper is safe to call from anywhere,
		// including from a Shipper composed literally.
		w = os.Stderr
	}
	return slog.New(slog.NewJSONHandler(w, nil)).With("logshipper", true)
}

// diagLogger returns the logger every diagnostic in this file writes through.
//
// The fallback covers a Shipper composed literally rather than through
// NewShipper, which sets s.diag for every Config. Building a logger per call is
// wasteful, but it only happens on a path that would otherwise have to either
// panic or swallow the diagnostic, and both are worse than an allocation.
func (s *Shipper) diagLogger() *slog.Logger {
	if s.diag != nil {
		return s.diag
	}
	return newDiagLogger(s.cfg)
}

// diagf writes exactly one Self_Diagnostic at level, with slog-style alternating
// key/value args.
//
// This is the single funnel every diagnostic in the package goes through, which
// is what keeps Req 8.1 and Req 8.2 verifiable by inspection: audit this one
// function and the marker attribute, the bypass of slog.Default(), and the
// absence of any enqueue are established for every caller.
//
// context.Background() is passed rather than a caller context on purpose. The
// diagnostic handler ignores the context entirely, and a cancelled request
// context must not be able to suppress a diagnostic about log delivery.
func (s *Shipper) diagf(level slog.Level, msg string, args ...any) {
	if s == nil {
		return
	}
	s.diagLogger().Log(context.Background(), level, msg, args...)
}

// diagPanic writes exactly one Self_Diagnostic describing a recovered panic,
// carrying the panic value truncated to maxDiagPanicChars characters and the
// stack truncated to maxDiagStackBytes bytes (Req 8.3, 8.5).
//
// It never enqueues and never touches the fan-out handler (Req 8.1, 8.2): the
// logger is bound straight to a writer, so a diagnostic about a failed
// conversion cannot itself produce a Log_Event.
func (s *Shipper) diagPanic(msg string, p any) {
	panicValue, stack := PanicDetails(p)
	s.diagf(slog.LevelError, msg,
		slog.String("panic", panicValue),
		slog.String("stack", stack),
	)
}

// PanicDetails returns the bounded text and current-goroutine stack used when
// logging a recovered panic. The value is limited to its first 1024 characters
// at a UTF-8 rune boundary, and the stack to its first 8192 bytes (Req 2.6,
// 8.3). It is exported so application recovery middleware can use exactly the
// same limits and semantics as Log_Shipper recovery diagnostics.
func PanicDetails(p any) (value, stack string) {
	return truncatedPanicValue(p), truncatedPanicStack()
}

// EmitStartupDiagnostics writes the deferred Diagnostics that ResolveConfig
// returned, at their recorded levels, and then — only for a Shipper that
// resolved to enabled — exactly one INFO startup summary.
//
// ResolveConfig is pure and writes nothing, so every configuration verdict it
// reached arrives here as data: the WARNs naming an unset BETTERSTACK_SOURCE_TOKEN
// or BETTERSTACK_INGEST_URL (Req 1.4, 1.5), the ERROR for an unusable ingest URL
// (Req 1.6), the ERROR for being disabled in production (Req 1.12), and the WARNs
// for each rejected numeric or level value (Req 2.8, 5.4, 5.6, 6.7). Emitting
// them here rather than inside ResolveConfig is what lets the whole validation
// table be unit-tested with no writer and no Shipper.
//
// main.go calls this between NewShipper and Start, so the summary describes a
// Shipper that is about to run and the process is still short of accepting its
// first inbound request (Req 1.7).
//
// No diagnostic written here can become a Log_Event (Req 11.8): they all go
// through diagf.
func (s *Shipper) EmitStartupDiagnostics(diags []Diagnostic) {
	if s == nil {
		return
	}
	for _, d := range diags {
		s.diagf(d.Level, d.Message, d.Args...)
	}

	// A disabled Shipper gets no summary: there is no threshold, batch size, or
	// endpoint worth reporting for a component that will transmit nothing, and
	// the deferred diagnostics above have already said why it is disabled
	// (Req 1.4 through 1.6, 1.12). State rather than cfg.Enabled is read because
	// state is what every other path consults; at startup it is either enabled
	// or disabled, since only a 402 response can suspend a Shipper and that
	// cannot have happened before Start.
	if s.state.Load() != stateEnabled {
		return
	}

	// Req 11.9: exactly one INFO diagnostic, carrying the Shipping_Threshold
	// name, the maximum Delivery_Batch size, the flush interval in seconds, the
	// Event_Queue capacity, and the ingest host — and excluding the Source_Token
	// and the endpoint query string. The host alone is emitted rather than the
	// sanitized scheme/host/path, because Req 11.9 names the host specifically
	// and a path can carry a source identifier.
	s.diagf(slog.LevelInfo, "better stack error forwarding enabled",
		slog.String("min_level", s.cfg.MinLevel.String()),
		slog.Int("batch_size", s.cfg.BatchSize),
		slog.Float64("flush_seconds", s.cfg.FlushEvery.Seconds()),
		slog.Int("queue_capacity", s.queueCapacity()),
		slog.String("ingest_host", ingestHost(s.cfg.IngestURL)),
	)
}

// maybeDiagDrop writes at most one WARN per dropDiagWindow about Log_Events
// discarded because the Event_Queue was at capacity, reporting the *cumulative*
// dropped count and the configured capacity (Req 6.4, 6.8).
//
// It is called from enqueue, which is on the request hot path and must stay
// wait-free (Req 5.2, 6.3), so two things are true of every call:
//
//  1. **The rate-limit decision takes no lock.** It is one atomic load plus, at
//     most, one compare-and-swap on dropDiag. Suppressed calls — which is all but
//     one per minute of a sustained overflow — therefore add a couple of atomic
//     operations to enqueue and return, performing no I/O at all.
//
//  2. **Nothing is held across the write.** The counters mutex is taken and
//     released inside Snapshot, before the diagnostic is formatted, so a slow or
//     blocked diagnostic writer cannot stall a concurrent counter update. The one
//     call per window that does write pays for a small write to the configured
//     writer, which is what Req 6.4 asks for.
//
// The cumulative count is read from a Snapshot rather than accumulated locally,
// which is what makes Req 6.8 hold with no extra bookkeeping: every drop
// suppressed during the window has already incremented the counter, so the next
// diagnostic reports it.
func (s *Shipper) maybeDiagDrop() {
	if s == nil {
		return
	}
	if !s.dropDiag.allow(s.diagNow(), dropDiagWindow) {
		return // Req 6.8: suppressed, and already counted for the next report
	}

	snap := s.Snapshot()
	s.diagf(slog.LevelWarn, "log events dropped: event queue at capacity",
		slog.Uint64("dropped_total", snap.Dropped),
		slog.Uint64("dropped_queue_full", snap.DroppedByReason[reasonQueueFull]),
		slog.Int("queue_capacity", s.queueCapacity()),
	)
}

// diagNow reads the injected clock, which is the only source of time any
// rate-limit window in this package may use (Req 12.2), so advancing a fake
// clock rather than wall-clock time drives the drop-diagnostic window in tests.
//
// The fallback covers a Shipper composed literally, whose clock field is nil;
// NewShipper always sets it through applyDefaults.
func (s *Shipper) diagNow() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return SystemClock().Now()
}

// queueCapacity reports the Event_Queue capacity to report in a diagnostic.
//
// The channel capacity is preferred over cfg.QueueSize because it is the
// capacity actually in force: NewShipper clamps a non-positive configured size
// to the default, and reporting the configured value would then describe a queue
// that does not exist. cfg.QueueSize is the fallback for a Shipper composed
// literally with no channel.
func (s *Shipper) queueCapacity() int {
	if s.queue != nil {
		return cap(s.queue)
	}
	return s.cfg.QueueSize
}

// dropDiagLimiter is the one-per-window gate on the queue-full drop diagnostic.
//
// Its whole state is a single atomic int64 holding the clock time of the most
// recent drop diagnostic in Unix nanoseconds, or dropDiagNever when none has
// been written. A mutex would also be correct, but it would put a lock on the
// enqueue path that every producer goroutine contends for during exactly the
// burst the limiter exists to describe; an atomic keeps the hot path wait-free
// (Req 6.3).
//
// The zero value is a usable limiter that has written nothing.
type dropDiagLimiter struct {
	lastNanos atomic.Int64
}

// allow reports whether a drop diagnostic may be written at now, and claims the
// window when it may.
//
// The claim is the point: many goroutines can be inside enqueue's default arm at
// the same instant, and Req 6.4 says *one* diagnostic per window, so deciding
// and recording have to be one indivisible step. A load followed by a separate
// store would let two goroutines both read a stale timestamp and both write.
// CompareAndSwap makes the winner unambiguous.
//
// The loop retries only when it lost a race, and a lost race means some other
// goroutine has just stamped a time it read from the same clock — so the next
// iteration sees a timestamp inside the window and returns false. It cannot spin
// indefinitely.
//
// A stamp at or before the stored one, which a clock that went backwards or two
// goroutines reading slightly different times can produce, yields a
// non-positive elapsed value and is treated as inside the window: when in doubt,
// suppress. Suppression loses no information, because the next diagnostic
// reports the cumulative count (Req 6.8).
func (l *dropDiagLimiter) allow(now time.Time, window time.Duration) bool {
	stamp := now.UnixNano()
	if stamp == dropDiagNever {
		// The instant that collides with the "never written" sentinel is the
		// Unix epoch exactly. Nudging it by one nanosecond keeps the sentinel
		// unambiguous. Accepted tradeoff, stated plainly: a clock positioned
		// at exactly the Unix epoch gets a rate-limit window one nanosecond
		// shorter than every other window. A real clock is never observed
		// there, so this is a documented theoretical gap, not a practical one.
		stamp = dropDiagNever + 1
	}
	for {
		last := l.lastNanos.Load()
		if last != dropDiagNever && stamp-last < int64(window) {
			return false
		}
		if l.lastNanos.CompareAndSwap(last, stamp) {
			return true
		}
	}
}

// diagEndpoint renders this Shipper's Ingest_Endpoint for inclusion in a
// diagnostic: scheme, host, and path only, with the query string and any
// userinfo component stripped (Req 10.5).
//
// It delegates to sanitizeEndpoint in ingest.go rather than repeating the
// rendering, so there is exactly one definition of what an endpoint looks like
// in output the operator sees. The ingest-failure diagnostics of tasks 8.1
// through 8.3 are its callers.
func (s *Shipper) diagEndpoint() string {
	return sanitizeEndpoint(s.cfg.IngestURL)
}

// ingestHost renders only the host — host and port, never userinfo — of an
// endpoint, which is what the startup summary reports (Req 11.9).
//
// It is distinct from sanitizeEndpoint because Req 11.9 names the host
// specifically, and the path of an ingest URL can itself carry a source
// identifier that has no business in a startup line.
//
// An endpoint that does not parse, or that carries no host, yields a fixed
// placeholder rather than the raw string, because the reason it failed to parse
// may be the very credential material this function exists to withhold.
// ResolveConfig rejects such a URL before a Shipper can be enabled (Req 1.6), so
// no enabled Shipper reaches the placeholder.
func ingestHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return unknownIngestHost
	}
	return parsed.Host
}

// truncatedPanicValue renders a recovered panic value as text and cuts it to its
// first maxDiagPanicChars characters.
//
// fmt.Sprint is used rather than a type switch because a panic value is any: it
// is commonly a string or an error, but it can be any value at all, including one
// whose String method panics — which fmt absorbs and renders inline rather than
// re-panicking, so rendering the value cannot turn a contained panic back into an
// escaping one.
func truncatedPanicValue(p any) string {
	return truncateChars(fmt.Sprint(p), maxDiagPanicChars)
}

// truncatedPanicStack captures the current goroutine's stack, cut to
// maxDiagStackBytes bytes.
//
// It is called from inside the deferred recovery, where the panicking frames are
// still described by the traceback, so the captured stack names the value that
// panicked and not merely the recovering function.
func truncatedPanicStack() string {
	buf := make([]byte, maxDiagStackBytes)
	return string(buf[:runtime.Stack(buf, false)])
}

// truncateChars returns the first max characters of s, cutting on a rune
// boundary so the result is never a broken UTF-8 sequence.
func truncateChars(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for offset := range s {
		if count == max {
			return s[:offset]
		}
		count++
	}
	return s
}
