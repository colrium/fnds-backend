package logging

// The FanoutHandler: the slog.Handler installed as slog.Default(), which hands
// every Log_Record to each of its children.
//
// There are exactly two children in production, in this order (see main.go):
//
//	1. the Console_Handler — slog.NewJSONHandler(os.Stdout, nil), unchanged from
//	   before this feature, which is what makes stdout byte-identical (Req 2.5)
//	2. the Better_Stack_Handler — *Handler, which converts and enqueues
//
// The whole file exists to make one guarantee mechanical: whatever the Better
// Stack path does — drop the event (Req 6.5), sit in disabled state (Req 1.10,
// 8.9), fail to reach the ingest endpoint (Req 8.6), or fail outright — the
// console child still writes its record to stdout. Three separate decisions below
// serve that single guarantee, and each is annotated with which one it is.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
)

var _ slog.Handler = (*FanoutHandler)(nil)

// FanoutHandler broadcasts each Log_Record to every child handler.
//
// A FanoutHandler is immutable after construction: WithAttrs and WithGroup return
// a new value over a freshly allocated children slice and never write through to
// the receiver, so one FanoutHandler is safe to share across goroutines — which
// slog.SetDefault guarantees it will be.
//
// Zero children is legal and is a no-op rather than an error, so a build that has
// not wired anything up yet degrades to silence rather than to a nil-pointer
// panic on the first log call.
type FanoutHandler struct {
	// children are called in this order, which is construction order.
	children []slog.Handler
}

// NewFanoutHandler builds a FanoutHandler over children, preserving the order
// given.
//
// The slice is copied, so a caller reusing its argument array cannot retune the
// fan-out after the fact. nil children are dropped here rather than tolerated
// forever: a caller that passes shipper.Handler() from a construction path that
// returned nil gets a working console, not a panic on the first record.
func NewFanoutHandler(children ...slog.Handler) *FanoutHandler {
	return &FanoutHandler{children: nonNilChildren(children)}
}

// nonNilChildren copies children into a freshly allocated slice, dropping nils.
//
// Freshly allocated is the point, not an incidental: every derivation goes
// through here, so no two FanoutHandlers can ever share a backing array and an
// append in one can never be visible in another. The capacity is deliberately
// exact rather than len(children) when a nil was dropped — this runs once per
// derivation, never per record.
func nonNilChildren(children []slog.Handler) []slog.Handler {
	out := make([]slog.Handler, 0, len(children))
	for _, child := range children {
		if child != nil {
			out = append(out, child)
		}
	}
	return slices.Clip(out)
}

// Enabled reports whether any child is enabled for l.
//
// **Decision 1: OR, not the first child's answer.** Returning the console child's
// answer alone would make the console the gate for the whole tree, so lowering or
// raising the console handler's level — a change whose author is thinking about
// stdout noise and nothing else — would silently change which records reach
// Better Stack. slog skips building the Record entirely when Enabled returns
// false, so that silencing would be total and invisible: no drop counter, no
// Self_Diagnostic, nothing. OR-ing means a child can only ever be gated by its
// own level, which is the property Req 1.10, 8.6, and 8.9 all depend on.
//
// The loop short-circuits on the first enabled child, so the ordinary case — the
// console child, whose threshold is the lowest — costs exactly one call.
//
// Unlike Handle, this deliberately does not recover from a child panic. Enabled
// is a predicate slog consults before it has a record to lose, so nothing is
// dropped by letting the panic escape; recovering here would instead have to
// invent an answer, and a false invented for a child whose Enabled panicked would
// silently and permanently disable that child. The panic-containment guarantee
// belongs to the delivery path, and that is where Handle applies it — including
// around its own Enabled calls, where a record *is* at stake.
func (h *FanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, child := range h.children {
		// The nil check is redundant against NewFanoutHandler, which drops nils,
		// and is kept because a FanoutHandler built as a struct literal inside
		// this package would otherwise panic here rather than at construction.
		if child != nil && child.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

// Handle offers r to every child, in construction order, and returns the joined
// errors of those that failed.
//
// Each child is gated on its *own* Enabled: the fan-out's Enabled already
// answered true because *some* child wants the record, so re-asking per child is
// what keeps a WARN record out of an ERROR-threshold Better Stack child while
// still writing it to stdout.
//
// **Decision 2: an error never short-circuits the loop.** Errors are collected
// and joined with errors.Join at the end rather than returned on first failure.
// Req 6.5 and 8.9 are both of the form "the Better Stack path failed, and the
// console record was written anyway", and a `return err` inside this loop is
// precisely the line that would break that for any ordering.
//
// **Decision 3: recover per child rather than once around the loop.** A single
// recover at the top of Handle would satisfy the letter of Req 8.8 — no panic
// escapes into the request goroutine — while still costing every child after the
// panicking one its record. Per-child recovery is what makes the guarantee
// ordering-independent: today the console child runs first so a Better Stack
// panic provably cannot reach it, but that is a fact about main.go's argument
// order, not about this type, and it is exactly the kind of fact a later reorder
// silently invalidates.
//
// The obvious objection to per-child recovery is that it hides bugs. It would, if
// it swallowed the panic — so it does not: the recovered value is turned into an
// error and joined into Handle's return value alongside the ordinary child
// errors, naming the child's position and type. slog.Logger discards Handle's
// error, so in production this converts a crash into a lost record for one child;
// the alternative converts it into a lost record for every later child plus,
// without Handler's own recovery, a failed HTTP request. Any handler wrapping
// this one, and every test below, sees the panic reported rather than absorbed.
//
// r is never mutated. slog.Record is passed by value, but Record.Clone exists
// because a Record shares its overflow attribute storage with its copies: a child
// calling r.AddAttrs directly could write into the array a later child reads
// through, and for the console child that would mean stdout bytes that no longer
// match the pre-feature handler's (Req 2.5). Nothing here appends to r, and a
// child that needs to add attrs must call r.Clone() first.
func (h *FanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	// errs stays nil for the common all-children-succeeded case, and
	// errors.Join(nil...) is nil, so a fan-out with zero children returns nil
	// without allocating.
	var errs []error
	for i, child := range h.children {
		if child == nil {
			continue
		}
		if err := handleChild(ctx, i, child, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// handleChild asks one child whether it wants r and, if so, hands it over,
// converting a panic from either call into an error.
//
// Both calls sit inside the recovery on purpose. A child whose Enabled panics
// would otherwise take down the rest of the loop from a position where a record
// really is at stake, which is the one case Enabled's own no-recover reasoning
// does not cover.
//
// The panic value is rendered with fmt rather than type-switched because a panic
// value is `any` and may be a value whose String method itself panics; fmt
// absorbs that and renders it inline instead of re-panicking, so describing the
// failure cannot become a second failure. The child's index and type are included
// because the fan-out's children are otherwise anonymous in a joined error.
func handleChild(ctx context.Context, i int, child slog.Handler, r slog.Record) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("logging: fanout child %d (%T) panicked: %v", i, child, p)
		}
	}()
	if !child.Enabled(ctx, r.Level) {
		return nil
	}
	return child.Handle(ctx, r)
}

// WithAttrs returns a FanoutHandler whose children each carry attrs.
//
// The empty batch returns the receiver, which the slog.Handler documentation
// permits as an optimization.
//
// Each child receives its own clone of the batch. The slog.Handler contract gives
// WithAttrs's callee ownership of the slice — it may retain, modify, or discard it
// — and handing one slice to N children would hand it to N owners, so a child
// that resolves values in place would be editing another child's attrs. Cloning
// costs one allocation per child per derivation, which happens at
// logger.With time and never per record.
func (h *FanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	derived := make([]slog.Handler, 0, len(h.children))
	for _, child := range h.children {
		if child == nil {
			continue
		}
		derived = append(derived, child.WithAttrs(slices.Clone(attrs)))
	}
	// Routed through nonNilChildren so a child whose WithAttrs returned nil
	// cannot plant one in the derived fan-out.
	return &FanoutHandler{children: nonNilChildren(derived)}
}

// WithGroup returns a FanoutHandler that has opened the group name on every
// child.
//
// The empty name returns the receiver, which the slog.Handler contract requires
// rather than merely permits.
func (h *FanoutHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	derived := make([]slog.Handler, 0, len(h.children))
	for _, child := range h.children {
		if child == nil {
			continue
		}
		derived = append(derived, child.WithGroup(name))
	}
	return &FanoutHandler{children: nonNilChildren(derived)}
}
