package logging

// The Better_Stack_Handler: the slog.Handler that filters Log_Records by
// severity and hands the survivors to the Shipper.
//
// This file holds the handler's state, its three cheap, allocation-light
// contract methods — Enabled, WithAttrs, WithGroup — and Handle, the only method
// that touches the Shipper.
//
// The state layout below exists to make the slog.Handler contract mechanical
// rather than remembered. Two rules carry all the weight:
//
//  1. A derived handler never shares an appendable slice with its parent, so two
//     siblings derived from one parent cannot write to the same backing array.
//     slices.Clip is what enforces that; see WithAttrs.
//  2. An attr batch is qualified by the groups that were open when it was added,
//     not by the groups open at Handle time. attrFrame (declared in convert.go,
//     beside the conversion that consumes it) is what records that binding.

import (
	"context"
	"log/slog"
	"slices"
)

var _ slog.Handler = (*Handler)(nil)

// Handler converts Log_Records at or above a threshold into Log_Events and hands
// them to a Shipper.
//
// A nil shipper is legal and is not a degenerate case: it produces a
// conversion-only handler that starts no goroutine and opens no connection, so
// the whole payload contract is testable in isolation (Req 12.9). Handle
// discards on a nil shipper; Enabled still answers the threshold question, so
// the level gate behaves identically either way.
//
// A Handler is immutable after construction. WithAttrs and WithGroup return a
// new value and never write through to the receiver, which is what makes it safe
// to share one handler across goroutines.
type Handler struct {
	shipper *Shipper     // nil is legal: conversion-only handler (Req 12.9)
	level   slog.Leveler // the Shipping_Threshold
	cfg     convertConfig
	groups  []string    // groups currently open, outermost first
	frames  []attrFrame // preformatted attrs, in add order
}

// NewHandler builds a Handler shipping records at or above level to s.
//
// s may be nil (Req 12.9). level may be nil, in which case the threshold falls
// back to ERROR, the same default ResolveConfig applies: a handler that panics
// on its first Enabled call would violate Req 8.8 over a construction slip.
func NewHandler(s *Shipper, level slog.Leveler) *Handler {
	h := &Handler{shipper: s, level: level}
	if level == nil {
		h.level = slog.LevelError
	}
	if s != nil {
		// Conversion depends on two strings only, so the handler projects them
		// once here rather than reaching into the Shipper on every record.
		h.cfg = newConvertConfig(s.cfg)
	}
	return h
}

// Enabled reports whether l is at or above the Shipping_Threshold.
//
// It is the threshold comparison and nothing else — no queue-depth check, no
// enabled-state check, no closed check. Req 2.9 and 2.10 define the level gate
// purely in terms of the threshold, and slog calls Enabled to decide whether to
// build the record at all: folding runtime state in here would make record
// construction depend on queue pressure, and would let a full queue silence the
// console child of the fan-out handler.
//
// ctx is unused, which the slog.Handler contract permits.
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

// Handle converts one Log_Record into a Log_Event, redacts it, caps its
// serialized size, and hands it to the Shipper.
//
// It returns nil from every path and propagates no panic for any record
// (Req 8.8). The nil return is not written out of the recovery below: an
// unrecovered-then-recovered function returns the zero values of its unnamed
// results, and the zero value of error is nil.
//
// Order of operations, and why each step is where it is:
//
//  1. **The two cheap exits.** A nil Shipper or a below-threshold level returns
//     before conversion, so no admission is counted, nothing is serialized, and
//     no event reaches the queue (Req 2.4, 12.9). This is the only path that
//     costs a level comparison and nothing else.
//
//  2. **addAdmitted, before conversion.** `enqueued` means *admitted for
//     shipping*, not *successfully sent to the channel*. A conversion panic has
//     to increment `dropped` (Req 8.5), so its matching admission must already be
//     counted or the Req 11.4 equality — enqueued == delivered + dropped +
//     in_flight + queue_depth — breaks on exactly the paths that matter most. See
//     the counting-discipline table in the design.
//
//  3. **The recover, above convertRecord.** convertRecord deliberately does not
//     recover (see the header of convert.go), so a value whose MarshalJSON or
//     Error method panics arrives here. Those two are the panics that genuinely
//     escape conversion; a panicking String is absorbed by fmt and a panicking
//     LogValue by slog.Value.Resolve, which convertRecord calls. Because the
//     recover sits above the Resolve call rather than inside conversion, a
//     LogValuer that found a way past Resolve's own recovery is contained too
//     (Req 8.8).
//
//  4. **convert, redact, cap, enqueue, in that order.** Redaction runs strictly
//     before enqueue so the Event_Queue holds zero unredacted events (Req 10.9),
//     and after Resolve so it sees the value a LogValuer actually produced
//     (Req 10.8). The size cap runs last because it is the only stage that can
//     state the final byte length; see the header of truncate.go.
//
// Correlation needs no work here: ctx is passed to convertRecord, which reads the
// request triple and the user id from it and omits `request` or `user` when either
// lookup misses, without failing the conversion (Req 4.5, 4.7, 4.10). A record
// emitted through slog.Error rather than slog.ErrorContext carries
// context.Background() and simply omits both.
//
// Nothing here consults shipper state, so a Log_Record emitted while Echo
// graceful shutdown is in progress is converted and enqueued like any other
// (Req 9.2); whether that event still ships is the close operation's decision.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if h.shipper == nil || !h.Enabled(ctx, r.Level) {
		return nil
	}
	s := h.shipper
	s.counters.addAdmitted(1)

	// handedOff records that the event reached enqueue, which is what keeps the
	// accounting honest if the panic came from enqueue rather than from
	// conversion. enqueue attributes its own drops — queue full, after close,
	// disabled — so counting a second drop here for a panic raised inside it
	// would push `dropped` above the admissions it is supposed to balance. Before
	// enqueue, nothing has been counted and nothing shared has been mutated, so
	// the drop is unambiguously this frame's to record.
	handedOff := false
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if !handedOff {
			// Req 8.5: one dropped event, attributed to the conversion panic,
			// plus the recovered-panic counter. The partially built event is
			// simply never enqueued — it is unreachable from any other
			// goroutine, so the Event_Queue length and every previously
			// enqueued event are untouched by construction rather than by
			// cleanup.
			s.counters.addDropped(reasonConversionPanic, 1)
			s.counters.addConversionPanic()
		}
		s.diagPanic("panic converting log record", p)
	}()

	ev := convertRecord(ctx, r, h.cfg, h.frames, h.groups) // may panic on hostile values
	// A Shipper without a Redactor is not a licence to ship unredacted values:
	// failing closed is the only safe reading of Req 10.9, and the real Shipper
	// always sets the field. The explicit nil check (rather than letting
	// Apply's own nil dereference panic) means the recovered diagnostic names
	// the actual problem instead of reporting an opaque "nil pointer
	// dereference" that gives the operator nothing to act on.
	if s.redactor == nil {
		panic("logging: Shipper has a nil Redactor; refusing to ship unredacted event")
	}
	s.redactor.Apply(ev)
	capEventSize(ev)

	handedOff = true
	s.enqueue(ev)
	return nil
}

// WithAttrs returns a Handler whose records carry attrs, nested under the groups
// open right now.
//
// The empty batch returns the receiver, which the slog.Handler documentation
// permits as an optimization.
//
// Everything else about this method is about not sharing a backing array. The
// classic bug is
//
//	h2.frames = append(h.frames, f)
//
// which is correct exactly when h.frames has no spare capacity. When it does,
// two handlers derived from the same parent — the common case for
// logger.With(...) called twice — append into the same array slot and the second
// derivation silently overwrites the first's attrs. slices.Clip returns a slice
// whose capacity equals its length, so the append below is guaranteed to
// reallocate and the parent's array is never written. h.groups is clipped for
// the same reason before being stored in the frame: the frame must keep the
// group path as it is now, even if a later WithGroup on the parent appends to it.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	h2 := *h
	h2.frames = append(slices.Clip(h.frames), attrFrame{
		groups: slices.Clip(h.groups),
		// Cloned because the batch belongs to the caller: slog hands over a
		// slice it is free to reuse, and a frame is immutable once constructed.
		attrs: slices.Clone(attrs),
	})
	return &h2
}

// WithGroup returns a Handler that opens the group name, so subsequent record
// attrs and subsequent WithAttrs batches nest one level deeper.
//
// The empty name returns the receiver, which the slog.Handler contract requires
// rather than merely permits.
//
// Both slices are clipped, not just groups. Clipping frames matters even though
// this method does not append to it: without it, this handler and a sibling
// derived from the same parent would share frames' spare capacity, and the first
// WithAttrs on either one would write into the other's array.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.groups = append(slices.Clip(h.groups), name)
	h2.frames = slices.Clip(h.frames)
	return &h2
}
