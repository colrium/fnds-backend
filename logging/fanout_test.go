package logging

// Tests for the FanoutHandler.
//
// The three decisions documented in fanout.go are the three things worth
// testing, and each maps to a requirement about the console child surviving a
// failure of the Better Stack child: the OR-of-children gate (Req 1.10, 8.6,
// 8.9), an error never short-circuiting the loop (Req 6.5), and a panic never
// doing so either (Req 6.5, 8.9). The remainder pins the slog.Handler contract:
// construction order, per-child gating, the two documented no-op derivations, and
// that r survives the fan-out unmutated (Req 2.5).
//
// Test names here are prefixed TestFanout and helpers fanout*, so task 4.5 can
// append the console byte-identity test to this file without collision.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fanoutTrace records child interactions in call order, across every child of
// one fan-out. Two separate logs rather than one, so an order assertion about
// Handle is not perturbed by the Enabled calls interleaved with it.
type fanoutTrace struct {
	enabled []string
	handled []string
}

// fanoutChild is a child handler that records what it was asked to do and can be
// told to fail in each of the ways the fan-out has to tolerate.
//
// level is the child's *own* gate, which is the whole point of the type: the
// production tree has a console child enabled from INFO and a Better Stack child
// enabled from ERROR, and almost every test here is about the records that fall
// between them.
type fanoutChild struct {
	name  string
	level slog.Level
	trace *fanoutTrace

	err            error // returned from Handle
	panicOnHandle  any   // non-nil: Handle panics with this value
	panicOnEnabled any   // non-nil: Enabled panics with this value

	// clobberBatch makes WithAttrs write through its argument slice, which the
	// slog.Handler contract permits ("the Handler owns the slice"). It is how the
	// per-child clone in FanoutHandler.WithAttrs is observed.
	clobberBatch bool

	handled []slog.Record // records this instance received
	attrs   []slog.Attr   // attrs accumulated through WithAttrs
	groups  []string      // groups accumulated through WithGroup
}

func newFanoutChild(trace *fanoutTrace, name string, level slog.Level) *fanoutChild {
	return &fanoutChild{name: name, level: level, trace: trace}
}

func (c *fanoutChild) Enabled(_ context.Context, l slog.Level) bool {
	c.trace.enabled = append(c.trace.enabled, c.name)
	if c.panicOnEnabled != nil {
		panic(c.panicOnEnabled)
	}
	return l >= c.level
}

func (c *fanoutChild) Handle(_ context.Context, r slog.Record) error {
	c.trace.handled = append(c.trace.handled, c.name)
	c.handled = append(c.handled, r)
	if c.panicOnHandle != nil {
		panic(c.panicOnHandle)
	}
	return c.err
}

func (c *fanoutChild) WithAttrs(attrs []slog.Attr) slog.Handler {
	if c.clobberBatch && len(attrs) > 0 {
		attrs[0] = slog.String("clobbered", "by-"+c.name)
	}
	derived := *c
	derived.attrs = append(slices.Clip(c.attrs), attrs...)
	return &derived
}

func (c *fanoutChild) WithGroup(name string) slog.Handler {
	derived := *c
	derived.groups = append(slices.Clip(c.groups), name)
	return &derived
}

// fanoutChildren asserts a derived handler is still a FanoutHandler and returns
// its children as *fanoutChild, which is the only way to see what a derivation
// did to each child.
func fanoutChildren(t *testing.T, h slog.Handler) []*fanoutChild {
	t.Helper()
	fan, ok := h.(*FanoutHandler)
	if !ok {
		t.Fatalf("derived handler has type %T, want *logging.FanoutHandler", h)
	}
	children := make([]*fanoutChild, 0, len(fan.children))
	for i, child := range fan.children {
		typed, ok := child.(*fanoutChild)
		if !ok {
			t.Fatalf("derived child %d has type %T, want *logging.fanoutChild", i, child)
		}
		children = append(children, typed)
	}
	return children
}

// fanoutAttrKeys renders a child's accumulated attrs as key=value, which is all
// the derivation tests constrain.
func fanoutAttrKeys(c *fanoutChild) []string {
	rendered := make([]string, 0, len(c.attrs))
	for _, a := range c.attrs {
		rendered = append(rendered, a.Key+"="+a.Value.String())
	}
	return rendered
}

// fanoutRecordKeys lists a record's attr keys in order, so "r was not mutated"
// is a claim about content rather than about a count alone.
func fanoutRecordKeys(r slog.Record) []string {
	keys := make([]string, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		keys = append(keys, a.Key)
		return true
	})
	return keys
}

func TestFanoutEnabledIsTheOrAcrossChildren(t *testing.T) {
	// Req 1.10, 8.6, 8.9: the console child is enabled from INFO and the Better
	// Stack child from ERROR. A WARN record must be reported enabled because the
	// console wants it, and — the direction that actually matters — the tree must
	// stay enabled for a level the *second* child wants even when the first does
	// not, so raising the console's level can never silence Better Stack.
	trace := new(fanoutTrace)
	console := newFanoutChild(trace, "console", slog.LevelInfo)
	shipper := newFanoutChild(trace, "shipper", slog.LevelError)

	cases := []struct {
		name     string
		children []slog.Handler
		level    slog.Level
		want     bool
	}{
		{"below both", []slog.Handler{console, shipper}, slog.LevelDebug, false},
		{"first child only", []slog.Handler{console, shipper}, slog.LevelInfo, true},
		{"both children", []slog.Handler{console, shipper}, slog.LevelError, true},
		// The console child raised above WARN: the shipper child still answers.
		{"second child only", []slog.Handler{
			newFanoutChild(trace, "quiet-console", slog.LevelError+4),
			shipper,
		}, slog.LevelError, true},
		{"no children", nil, slog.LevelError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewFanoutHandler(tc.children...)
			if got := h.Enabled(context.Background(), tc.level); got != tc.want {
				t.Errorf("Enabled(%s) = %t, want %t", tc.level, got, tc.want)
			}
		})
	}
}

func TestFanoutHandleCallsChildrenInConstructionOrder(t *testing.T) {
	// main.go puts the console child first, and Req 2.5's byte-identity claim is
	// about output that has already been written by the time the Better Stack
	// child does anything at all. Order is therefore part of the contract, not an
	// implementation detail.
	trace := new(fanoutTrace)
	first := newFanoutChild(trace, "first", slog.LevelInfo)
	second := newFanoutChild(trace, "second", slog.LevelInfo)
	third := newFanoutChild(trace, "third", slog.LevelInfo)

	h := NewFanoutHandler(first, second, third)
	if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "ordered")); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	if got, want := trace.handled, []string{"first", "second", "third"}; !equalStrings(got, want) {
		t.Errorf("children handled in order %v, want %v", got, want)
	}
}

func TestFanoutHandleSkipsAChildWhoseEnabledIsFalse(t *testing.T) {
	// The per-child gate: a WARN record reaches stdout and must not reach an
	// ERROR-threshold Better Stack child, even though the fan-out's own Enabled
	// said true because the console child wanted it.
	trace := new(fanoutTrace)
	console := newFanoutChild(trace, "console", slog.LevelInfo)
	shipper := newFanoutChild(trace, "shipper", slog.LevelError)

	h := NewFanoutHandler(console, shipper)
	if err := h.Handle(context.Background(), newTestRecord(slog.LevelWarn, "warn only")); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	if got, want := trace.handled, []string{"console"}; !equalStrings(got, want) {
		t.Errorf("children handled = %v, want %v: the shipper child is below its own threshold", got, want)
	}
	// Both were asked, which is what makes the skip a decision rather than an
	// accident of the fan-out's own gate.
	if got, want := trace.enabled, []string{"console", "shipper"}; !equalStrings(got, want) {
		t.Errorf("Enabled queried on %v, want %v: each child answers for itself", got, want)
	}
	if len(shipper.handled) != 0 {
		t.Errorf("shipper child received %d records, want 0", len(shipper.handled))
	}
}

func TestFanoutHandleJoinsChildErrorsAndStillRunsEveryChild(t *testing.T) {
	// Req 6.5: a failure on one path never costs another path its record. A
	// `return err` inside the loop is the line this test exists to catch, so the
	// failing child is placed first.
	failFirst := errors.New("first child failed")
	failThird := errors.New("third child failed")

	trace := new(fanoutTrace)
	first := newFanoutChild(trace, "first", slog.LevelInfo)
	first.err = failFirst
	second := newFanoutChild(trace, "second", slog.LevelInfo)
	third := newFanoutChild(trace, "third", slog.LevelInfo)
	third.err = failThird

	h := NewFanoutHandler(first, second, third)
	err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "partly failed"))

	if err == nil {
		t.Fatal("Handle returned nil, want the joined child errors")
	}
	if !errors.Is(err, failFirst) {
		t.Errorf("Handle error %v does not wrap the first child's error", err)
	}
	if !errors.Is(err, failThird) {
		t.Errorf("Handle error %v does not wrap the third child's error", err)
	}
	if got, want := trace.handled, []string{"first", "second", "third"}; !equalStrings(got, want) {
		t.Errorf("children handled = %v, want %v: an error short-circuited the fan-out", got, want)
	}
}

func TestFanoutHandlePanickingChildDoesNotPreventALaterChild(t *testing.T) {
	// Req 6.5, 8.9, ordering-independently. The panicking child is placed first
	// on purpose: today main.go puts the console child first so a Better Stack
	// panic provably cannot reach it, but that is a fact about main.go's argument
	// order rather than about this type.
	//
	// The recovered panic must also be *reported*, not swallowed — that is what
	// makes per-child recovery a containment decision rather than a way to hide a
	// bug.
	cases := []struct {
		name           string
		panicOnHandle  any
		panicOnEnabled any
	}{
		{name: "Handle panics", panicOnHandle: "child handle exploded"},
		{name: "Enabled panics", panicOnEnabled: "child enabled exploded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := new(fanoutTrace)
			hostile := newFanoutChild(trace, "hostile", slog.LevelInfo)
			hostile.panicOnHandle = tc.panicOnHandle
			hostile.panicOnEnabled = tc.panicOnEnabled
			survivor := newFanoutChild(trace, "survivor", slog.LevelInfo)

			h := NewFanoutHandler(hostile, survivor)

			// Reaching the next line at all is half the assertion: no panic
			// escaped into this goroutine.
			err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "hostile child"))

			if len(survivor.handled) != 1 {
				t.Fatalf("later child received %d records, want 1: a panic short-circuited the fan-out", len(survivor.handled))
			}
			if err == nil {
				t.Fatal("Handle returned nil, want the recovered panic reported as an error")
			}
			if !strings.Contains(err.Error(), "panicked") {
				t.Errorf("Handle error = %q, want it to describe a recovered panic", err)
			}
			// The child is named by position and type, since the fan-out's
			// children are otherwise anonymous in a joined error.
			if !strings.Contains(err.Error(), "child 0") {
				t.Errorf("Handle error = %q, want it to identify the panicking child's position", err)
			}

			// And the fan-out still works: the hostile child is not disabled, it
			// just fails again, and the survivor keeps receiving records.
			_ = h.Handle(context.Background(), newTestRecord(slog.LevelError, "again"))
			if len(survivor.handled) != 2 {
				t.Errorf("later child received %d records, want 2 after a second hostile record", len(survivor.handled))
			}
		})
	}
}

func TestFanoutHandleDoesNotMutateTheIncomingRecord(t *testing.T) {
	// Req 2.5: the console child's bytes must match the pre-feature handler's, so
	// what the second child does cannot be allowed to change what the first child
	// saw. slog.Record is passed by value but shares its overflow attribute
	// storage with its copies, so the discipline is that a child adding attrs
	// clones first — asserted here through a child that does exactly that.
	trace := new(fanoutTrace)
	cloner := newFanoutChild(trace, "cloner", slog.LevelInfo)
	observer := newFanoutChild(trace, "observer", slog.LevelInfo)

	record := newTestRecord(slog.LevelError, "shared record",
		"a", 1, "b", 2, "c", 3, "d", 4, "e", 5, "f", 6)
	want := fanoutRecordKeys(record)

	// A well-behaved child that needs to add attrs: clone, then append to the
	// clone. cloner.Handle records the record it was given, so its own view is
	// checked below too.
	h := NewFanoutHandler(cloner, observer)
	cloneAndAdd := func(r slog.Record) {
		clone := r.Clone()
		clone.AddAttrs(slog.String("added-by", "cloner"))
		if got := clone.NumAttrs(); got != len(want)+1 {
			t.Errorf("clone holds %d attrs, want %d", got, len(want)+1)
		}
	}

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}
	cloneAndAdd(cloner.handled[0])

	if got := fanoutRecordKeys(observer.handled[0]); !equalStrings(got, want) {
		t.Errorf("second child saw attrs %v, want %v: the first child's work leaked", got, want)
	}
	if got := fanoutRecordKeys(record); !equalStrings(got, want) {
		t.Errorf("caller's record holds attrs %v after Handle, want %v", got, want)
	}
	if got, want := record.Message, "shared record"; got != want {
		t.Errorf("caller's record message = %q, want %q", got, want)
	}
}

func TestFanoutWithAttrsFansOutToIndependentHandlers(t *testing.T) {
	trace := new(fanoutTrace)
	console := newFanoutChild(trace, "console", slog.LevelInfo)
	shipper := newFanoutChild(trace, "shipper", slog.LevelError)
	parent := NewFanoutHandler(console, shipper)

	left := parent.WithAttrs([]slog.Attr{slog.String("side", "left")})
	right := parent.WithAttrs([]slog.Attr{slog.String("side", "right")})

	// Every child of each derivation carries that derivation's attrs and only
	// those, and the parent's children carry none.
	for name, derived := range map[string]slog.Handler{"left": left, "right": right} {
		children := fanoutChildren(t, derived)
		if len(children) != 2 {
			t.Fatalf("%s derivation has %d children, want 2", name, len(children))
		}
		for _, child := range children {
			if got, want := fanoutAttrKeys(child), []string{"side=" + name}; !equalStrings(got, want) {
				t.Errorf("%s derivation, child %q attrs = %v, want %v", name, child.name, got, want)
			}
		}
	}
	if len(console.attrs) != 0 || len(shipper.attrs) != 0 {
		t.Errorf("parent's children gained attrs (%v, %v): WithAttrs wrote through to the receiver",
			fanoutAttrKeys(console), fanoutAttrKeys(shipper))
	}

	// A freshly allocated children slice, so no derivation shares a backing array
	// with the parent or with a sibling. Read through the concrete type rather
	// than through fanoutChildren, which allocates a slice of its own.
	leftFan, rightFan := left.(*FanoutHandler), right.(*FanoutHandler)
	if &leftFan.children[0] == &rightFan.children[0] {
		t.Error("sibling derivations share a children backing array")
	}
	if &leftFan.children[0] == &parent.children[0] {
		t.Error("a derivation shares its children backing array with the parent")
	}

	t.Run("empty batch returns the receiver", func(t *testing.T) {
		for name, attrs := range map[string][]slog.Attr{"nil batch": nil, "empty batch": {}} {
			if got := parent.WithAttrs(attrs); got != slog.Handler(parent) {
				t.Errorf("WithAttrs(%s) returned a new handler %p, want the receiver %p", name, got, parent)
			}
		}
	})

	t.Run("each child gets its own copy of the batch", func(t *testing.T) {
		// The slog.Handler contract gives WithAttrs's callee ownership of the
		// slice, so a child is entitled to write through it. Handing one slice to
		// both children would let the first edit the second's attrs.
		trace := new(fanoutTrace)
		greedy := newFanoutChild(trace, "greedy", slog.LevelInfo)
		greedy.clobberBatch = true
		polite := newFanoutChild(trace, "polite", slog.LevelInfo)

		derived := NewFanoutHandler(greedy, polite).WithAttrs([]slog.Attr{slog.String("k", "v")})

		children := fanoutChildren(t, derived)
		if got, want := fanoutAttrKeys(children[1]), []string{"k=v"}; !equalStrings(got, want) {
			t.Errorf("second child attrs = %v, want %v: the children shared one batch", got, want)
		}
	})
}

func TestFanoutWithGroupFansOutToIndependentHandlers(t *testing.T) {
	trace := new(fanoutTrace)
	console := newFanoutChild(trace, "console", slog.LevelInfo)
	shipper := newFanoutChild(trace, "shipper", slog.LevelError)
	parent := NewFanoutHandler(console, shipper)

	inner := parent.WithGroup("outer").WithGroup("inner")
	other := parent.WithGroup("other")

	for _, child := range fanoutChildren(t, inner) {
		if got, want := child.groups, []string{"outer", "inner"}; !equalStrings(got, want) {
			t.Errorf("child %q groups = %v, want %v", child.name, got, want)
		}
	}
	for _, child := range fanoutChildren(t, other) {
		if got, want := child.groups, []string{"other"}; !equalStrings(got, want) {
			t.Errorf("child %q groups = %v, want %v", child.name, got, want)
		}
	}
	if len(console.groups) != 0 || len(shipper.groups) != 0 {
		t.Errorf("parent's children gained groups (%v, %v): WithGroup wrote through to the receiver",
			console.groups, shipper.groups)
	}

	// Required by the slog.Handler contract rather than merely permitted.
	if got := parent.WithGroup(""); got != slog.Handler(parent) {
		t.Errorf(`WithGroup("") returned a new handler %p, want the receiver %p`, got, parent)
	}
}

func TestFanoutZeroChildrenIsANoOp(t *testing.T) {
	// A build that has not wired anything up yet degrades to silence, not to a
	// nil-pointer panic on the first log call.
	h := NewFanoutHandler()

	if h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled(ERROR) = true with no children, want false")
	}
	if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "nowhere to go")); err != nil {
		t.Errorf("Handle returned %v with no children, want nil", err)
	}
	if got := h.WithGroup("g"); got == nil {
		t.Error("WithGroup returned nil with no children")
	}
	if got := h.WithAttrs([]slog.Attr{slog.String("k", "v")}); got == nil {
		t.Error("WithAttrs returned nil with no children")
	}
}

func TestFanoutNilChildrenAreSkipped(t *testing.T) {
	// A caller passing shipper.Handler() from a construction path that returned
	// nil gets a working console, not a panic on the first record.
	trace := new(fanoutTrace)
	child := newFanoutChild(trace, "child", slog.LevelInfo)

	h := NewFanoutHandler(nil, child, nil)
	if got := len(h.children); got != 1 {
		t.Errorf("NewFanoutHandler kept %d children, want 1: nils are dropped at construction", got)
	}

	// The same tolerance for a struct built inside the package, which is what the
	// nil checks inside the loops cover.
	literal := &FanoutHandler{children: []slog.Handler{nil, child}}

	for name, fan := range map[string]*FanoutHandler{"constructed": h, "struct literal": literal} {
		t.Run(name, func(t *testing.T) {
			if !fan.Enabled(context.Background(), slog.LevelError) {
				t.Error("Enabled(ERROR) = false, want true from the non-nil child")
			}
			if err := fan.Handle(context.Background(), newTestRecord(slog.LevelError, "with a nil sibling")); err != nil {
				t.Errorf("Handle returned %v, want nil", err)
			}
			if got := len(fanoutChildren(t, fan.WithAttrs([]slog.Attr{slog.String("k", "v")}))); got != 1 {
				t.Errorf("derived fan-out has %d children, want 1", got)
			}
			if got := len(fanoutChildren(t, fan.WithGroup("g"))); got != 1 {
				t.Errorf("derived fan-out has %d children, want 1", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Console byte-identity (Req 2.5, 6.5, 8.9)
// ---------------------------------------------------------------------------
//
// The single most important promise this feature makes to the existing system:
// installing Better Stack forwarding does not change one byte of what already
// goes to stdout. The test below pins it the only way that means anything —
// against the pre-feature handler itself, `slog.NewJSONHandler(w, nil)`, run
// side by side with the fan-out over the same record.
//
// Every case is the cross product of three axes, because Req 2.5 has to hold for
// all of them at once:
//
//   - the record: levels from below DEBUG to above ERROR, empty and multi-byte
//     and invalid-UTF-8 messages, no attrs and overflow-storage-many attrs,
//     nested groups, reserved top-level keys, and a record carrying a program
//     counter;
//   - the derivation: the bare handler, and handlers reached through
//     WithAttrs/WithGroup in the combinations that make attribute nesting
//     interesting, since `logger.With(...)` fans out to both children;
//   - the state of the Better Stack child: accepting the record, dropping it for
//     being below its own threshold, dropping it because the Event_Queue is at
//     capacity (Req 6.5), and failing outright because its Shipper is unusable
//     (Req 8.9) — plus the plain error and plain panic cases, and no Better
//     Stack child at all.
//
// Two notes on what is and is not being compared.
//
// `slog.JSONHandler` writes a `time` field taken from `Record.Time`, so every
// record is built once, with the fixed timestamp newTestRecord supplies, and the
// same value is handed to both sides; a `logger.Error(...)` call site would stamp
// time.Now() and make the comparison meaningless. `source` is only emitted under
// `AddSource: true`, which neither side sets, so the program-counter case below
// is asserting that a PC changes nothing rather than that it renders — it is in
// the table because the pre-feature configuration is what defines the answer, and
// a future console handler gaining AddSource would have to keep the two sides
// equal.
//
// **The tension in Req 2.5.** Req 2.5 asks for two things that conflict at
// DEBUG: "every Log_Record of every level" on stdout, and bytes identical to the
// pre-feature `slog.NewJSONHandler(os.Stdout, nil)` — whose options default to
// `LevelInfo`, so DEBUG produces no output today. The design chose byte-identity
// as the operative constraint and passes `nil` options through unchanged, so
// DEBUG stays suppressed exactly as it is now. This test pins *that* decision:
// the DEBUG and below-DEBUG rows assert no bytes on either side, and the
// reference-side check below makes "wrote nothing" a positive assertion rather
// than a silently vacuous comparison. The choice is moot in practice — the repo
// contains zero `slog.Debug` and zero `slog.Log` call sites — and if DEBUG on
// stdout is ever wanted, the console handler needs an explicit
// `&slog.HandlerOptions{Level: slog.LevelDebug}` and these rows are the ones that
// will fail and demand a decision.

// errFanoutConsoleShipper is the failure a Better Stack child reports when it is
// standing in for a broken delivery path (Req 6.5, 8.9).
var errFanoutConsoleShipper = errors.New("better stack child failed")

// fanoutEmit reproduces what slog.Logger does with an already-built Record:
// consult Enabled, and hand the record over only when it says yes.
//
// slog.Logger has no exported way to emit a Record whose timestamp the caller
// chose, and dt is one of the bytes under comparison, so the record is built by
// hand and driven through both handlers here. The Enabled call is not incidental
// — it is the gate that makes DEBUG suppression observable, and skipping it would
// hand the console child a record slog would never have given it.
func fanoutEmit(h slog.Handler, r slog.Record) error {
	ctx := context.Background()
	if !h.Enabled(ctx, r.Level) {
		return nil
	}
	return h.Handle(ctx, r)
}

// fanoutConsoleRecord is one Log_Record pushed through both handlers.
//
// The record is built once per subtest and the same value goes to both sides,
// which keeps a program counter and a timestamp identical across the comparison
// and incidentally means a handler that mutated the record's shared overflow
// attribute storage would be caught here as well as by
// TestFanoutHandleDoesNotMutateTheIncomingRecord.
type fanoutConsoleRecord struct {
	name  string
	build func() slog.Record
}

func fanoutConsoleRecords() []fanoutConsoleRecord {
	return []fanoutConsoleRecord{
		{
			name:  "error with no attrs",
			build: func() slog.Record { return newTestRecord(slog.LevelError, "boom") },
		},
		{
			name: "error with attrs held in the record's front storage",
			build: func() slog.Record {
				return newTestRecord(slog.LevelError, "front storage",
					"a", 1, "b", "two", "c", 3.5, "d", true)
			},
		},
		{
			// Past slog.Record's inline front array, so the attrs live in
			// heap-allocated overflow storage shared with every copy of the
			// record — the storage Req 2.5 depends on nobody appending to.
			name: "error with enough attrs to spill into overflow storage",
			build: func() slog.Record {
				args := make([]any, 0, 24)
				for i := 0; i < 12; i++ {
					args = append(args, string(rune('a'+i)), i)
				}
				return newTestRecord(slog.LevelError, "overflow storage", args...)
			},
		},
		{
			// Below the Better Stack threshold used throughout this test, so the
			// Better Stack child declines the record and stdout must be
			// unaffected.
			name:  "warn, below the Better Stack threshold",
			build: func() slog.Record { return newTestRecord(slog.LevelWarn, "warned", "k", "v") },
		},
		{
			name:  "info, below the Better Stack threshold",
			build: func() slog.Record { return newTestRecord(slog.LevelInfo, "informed", "k", "v") },
		},
		{
			name:  "between WARN and ERROR",
			build: func() slog.Record { return newTestRecord(slog.LevelWarn+1, "custom level", "k", "v") },
		},
		{
			name:  "above ERROR",
			build: func() slog.Record { return newTestRecord(slog.LevelError+4, "louder than error", "k", "v") },
		},
		{
			// The Req 2.5 tension, pinned: no output on either side.
			name:  "debug, suppressed identically on both sides",
			build: func() slog.Record { return newTestRecord(slog.LevelDebug, "debugged", "k", "v") },
		},
		{
			name:  "below DEBUG, suppressed identically on both sides",
			build: func() slog.Record { return newTestRecord(slog.LevelDebug-4, "very quiet") },
		},
		{
			name:  "empty message",
			build: func() slog.Record { return newTestRecord(slog.LevelError, "") },
		},
		{
			name: "multi-byte message and multi-byte values",
			build: func() slog.Record {
				return newTestRecord(slog.LevelError, "ケニアの仕事 — café ✅",
					"jina", "wakili", "emoji", "🔥🔥")
			},
		},
		{
			// Neither side owns the escaping rules for a broken UTF-8 sequence;
			// both must apply the same ones.
			name: "invalid UTF-8 in the message and in a value",
			build: func() slog.Record {
				return newTestRecord(slog.LevelError, "bad \xff\xfe bytes",
					"value", "also \xc3 broken", "bytes", []byte{0x00, 0xff, 0x7f})
			},
		},
		{
			name: "nested groups",
			build: func() slog.Record {
				return newTestRecord(slog.LevelError, "grouped",
					slog.String("top", "1"),
					slog.Group("db",
						slog.String("query", "SELECT 1"),
						slog.Group("inner", slog.Int("n", 2)),
					),
				)
			},
		},
		{
			// The keys the Better Stack child relocates under `context`
			// (Req 3.10). The console child is not allowed to notice: duplicate
			// JSON keys and all, its bytes are whatever the pre-feature handler
			// produced.
			name: "attr keys colliding with reserved top-level fields",
			build: func() slog.Record {
				return newTestRecord(slog.LevelError, "reserved keys",
					"dt", "not-a-timestamp", "level", "nope", "message", "shadow",
					"service", "other", "context", 1, "truncated", false)
			},
		},
		{
			name: "nil and error-valued attrs",
			build: func() slog.Record {
				return newTestRecord(slog.LevelError, "odd values",
					slog.Any("nothing", nil),
					slog.Any("failure", errors.New("inner failure")),
					slog.Any("empty group", slog.Group("g")),
				)
			},
		},
		{
			// A live call site. `source` is only emitted with AddSource: true,
			// which neither side sets, so the assertion is that the PC changes
			// nothing — and that both sides agree about that.
			name: "record carrying a program counter",
			build: func() slog.Record {
				r := newTestRecord(slog.LevelError, "with a call site", "k", "v")
				var pcs [1]uintptr
				runtime.Callers(1, pcs[:])
				r.PC = pcs[0]
				return r
			},
		},
	}
}

// fanoutConsoleDerivation applies the same WithAttrs/WithGroup sequence to both
// sides. `logger.With(...)` on the fan-out fans out to every child, so Req 2.5
// has to survive derivation as well as direct use.
type fanoutConsoleDerivation struct {
	name   string
	derive func(*slog.Logger) *slog.Logger
}

func fanoutConsoleDerivations() []fanoutConsoleDerivation {
	return []fanoutConsoleDerivation{
		{"no derivation", func(l *slog.Logger) *slog.Logger { return l }},
		{"WithAttrs", func(l *slog.Logger) *slog.Logger {
			return l.With("component", "fanout", "n", 7)
		}},
		{"WithGroup", func(l *slog.Logger) *slog.Logger {
			return l.WithGroup("request")
		}},
		{"attrs, then a group, then more attrs", func(l *slog.Logger) *slog.Logger {
			return l.With("a", 1).WithGroup("g").With("b", 2)
		}},
		{"nested groups", func(l *slog.Logger) *slog.Logger {
			return l.WithGroup("outer").WithGroup("inner").With("k", "v")
		}},
		{"the documented no-op derivations", func(l *slog.Logger) *slog.Logger {
			return l.WithGroup("").With()
		}},
	}
}

// fanoutConsoleChild is the state of the Better Stack child for one run of the
// table: the handler itself, plus a check that it really did the thing the row
// is named for. Without the check a row could pass while the child silently did
// nothing at all, which would make the byte comparison prove much less than it
// appears to.
type fanoutConsoleChild struct {
	name  string
	build func(t *testing.T) (child slog.Handler, verify func(t *testing.T, r slog.Record, err error))
}

// fanoutConsoleThreshold is the Shipping_Threshold every Better Stack child in
// this table uses, matching the production default.
const fanoutConsoleThreshold = slog.LevelError

func fanoutConsoleChildren() []fanoutConsoleChild {
	// A child that panics from Enabled is deliberately absent. FanoutHandler.
	// Enabled does not recover — by the reasoning in fanout.go, nothing is at
	// stake there because no record has been built yet — so such a child never
	// reaches Handle and there is no console write to compare. Its containment
	// inside Handle is covered by
	// TestFanoutHandlePanickingChildDoesNotPreventALaterChild.
	return []fanoutConsoleChild{
		{
			name: "no Better Stack child at all",
			build: func(*testing.T) (slog.Handler, func(*testing.T, slog.Record, error)) {
				// The pre-feature tree, expressed through the new type: the
				// fan-out must be transparent even with nothing behind it.
				return nil, func(t *testing.T, _ slog.Record, err error) {
					if err != nil {
						t.Errorf("Handle returned %v with no Better Stack child, want nil", err)
					}
				}
			},
		},
		{
			name: "Better Stack child accepts and enqueues",
			build: func(t *testing.T) (slog.Handler, func(*testing.T, slog.Record, error)) {
				shipper, _ := newHandleTestShipper(t, 8)
				return NewHandler(shipper, fanoutConsoleThreshold),
					func(t *testing.T, r slog.Record, err error) {
						if err != nil {
							t.Errorf("Handle returned %v, want nil", err)
						}
						want := 0
						if r.Level >= fanoutConsoleThreshold {
							want = 1
						}
						if got := len(queuedEvents(shipper)); got != want {
							t.Errorf("Event_Queue holds %d events, want %d: the Better Stack path did not run", got, want)
						}
					}
			},
		},
		{
			name: "Better Stack Event_Queue at capacity, event dropped",
			build: func(t *testing.T) (slog.Handler, func(*testing.T, slog.Record, error)) {
				// Req 6.5: a queue with its single slot already taken, so the
				// next enqueue takes the wait-free default arm and drops.
				shipper, _ := newHandleTestShipper(t, 1)
				shipper.queue <- &Event{Message: "occupies the only slot"}

				return NewHandler(shipper, fanoutConsoleThreshold),
					func(t *testing.T, r slog.Record, err error) {
						if err != nil {
							t.Errorf("Handle returned %v, want nil: a dropped event is not an error", err)
						}
						wantDropped := uint64(0)
						if r.Level >= fanoutConsoleThreshold {
							wantDropped = 1
						}
						snap := shipper.counters.snapshot()
						if got := snap.DroppedByReason[reasonQueueFull]; got != wantDropped {
							t.Errorf("queue-full drops = %d, want %d: the Better Stack path did not run", got, wantDropped)
						}
						// Only the event planted above, so the drop really was a
						// drop rather than a second slot appearing.
						if got := len(queuedEvents(shipper)); got != 1 {
							t.Errorf("Event_Queue holds %d events, want 1 (the planted one)", got)
						}
					}
			},
		},
		{
			name: "Better Stack child returns an error",
			build: func(*testing.T) (slog.Handler, func(*testing.T, slog.Record, error)) {
				// "did the child run" is asked of the trace rather than of the
				// child, because fanoutChild's derivations return a copy of
				// themselves: under a WithAttrs/WithGroup row the record lands on
				// the copy the fan-out holds, and the original's handled slice
				// stays empty. The trace is shared by pointer across every
				// derivation, so it counts them all.
				trace := new(fanoutTrace)
				child := newFanoutChild(trace, "shipper", fanoutConsoleThreshold)
				child.err = errFanoutConsoleShipper
				return child, func(t *testing.T, r slog.Record, err error) {
					if r.Level < fanoutConsoleThreshold {
						if err != nil {
							t.Errorf("Handle returned %v for a below-threshold record, want nil", err)
						}
						if got := len(trace.handled); got != 0 {
							t.Errorf("Better Stack child received %d records below its threshold, want 0", got)
						}
						return
					}
					if !errors.Is(err, errFanoutConsoleShipper) {
						t.Errorf("Handle error = %v, want it to wrap the child's error", err)
					}
					if got := len(trace.handled); got != 1 {
						t.Errorf("Better Stack child received %d records, want 1", got)
					}
				}
			},
		},
		{
			name: "Better Stack child panics in Handle",
			build: func(*testing.T) (slog.Handler, func(*testing.T, slog.Record, error)) {
				trace := new(fanoutTrace)
				child := newFanoutChild(trace, "shipper", fanoutConsoleThreshold)
				child.panicOnHandle = "better stack child exploded"
				return child, func(t *testing.T, r slog.Record, err error) {
					if r.Level < fanoutConsoleThreshold {
						if err != nil {
							t.Errorf("Handle returned %v for a below-threshold record, want nil", err)
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), "panicked") {
						t.Errorf("Handle error = %v, want a reported panic", err)
					}
					if got := len(trace.handled); got != 1 {
						t.Errorf("Better Stack child received %d records, want 1", got)
					}
				}
			},
		},
		{
			name: "Better Stack Shipper unusable, panic escapes its Handle",
			build: func(t *testing.T) (slog.Handler, func(*testing.T, slog.Record, error)) {
				// Req 8.9, in its harshest form: a Shipper missing its counters.
				// The first thing Handler.Handle touches is s.counters.
				// addAdmitted, so the record dies on a nil dereference before
				// conversion, and the drop accounting in Handle's own recovery
				// dereferences the same nil — so the panic escapes Handle
				// entirely and lands on the fan-out's per-child recovery. Which
				// is the point: the console child has already written its bytes
				// by then, and nothing the Better Stack path does afterwards can
				// reach into stdout.
				shipper, _ := newHandleTestShipper(t, 8)
				shipper.counters = nil

				return NewHandler(shipper, fanoutConsoleThreshold),
					func(t *testing.T, r slog.Record, err error) {
						if r.Level < fanoutConsoleThreshold {
							if err != nil {
								t.Errorf("Handle returned %v for a below-threshold record, want nil", err)
							}
							return
						}
						if err == nil || !strings.Contains(err.Error(), "panicked") {
							t.Errorf("Handle error = %v, want a reported panic from the Better Stack child", err)
						}
						if err != nil && !strings.Contains(err.Error(), "child 1") {
							t.Errorf("Handle error = %q, want it to name the Better Stack child's position", err)
						}
					}
			},
		},
	}
}

func TestFanoutConsoleBytesMatchABareJSONHandler(t *testing.T) {
	// Req 2.5, 6.5, 8.9. See the commentary above this test for the three axes
	// and for the Req 2.5 DEBUG tension these rows pin.
	for _, child := range fanoutConsoleChildren() {
		t.Run(child.name, func(t *testing.T) {
			for _, derivation := range fanoutConsoleDerivations() {
				t.Run(derivation.name, func(t *testing.T) {
					for _, record := range fanoutConsoleRecords() {
						t.Run(record.name, func(t *testing.T) {
							var reference, console bytes.Buffer

							// The pre-feature tree: slog.SetDefault(slog.New(
							// slog.NewJSONHandler(os.Stdout, nil))).
							bare := derivation.derive(slog.New(slog.NewJSONHandler(&reference, nil)))

							// The tree this feature installs, with the console
							// child first, exactly as main.go orders them.
							betterStack, verify := child.build(t)
							fan := derivation.derive(slog.New(
								NewFanoutHandler(slog.NewJSONHandler(&console, nil), betterStack),
							))

							// One record, both sides, so dt and PC are identical
							// by construction rather than by luck.
							r := record.build()
							if err := fanoutEmit(bare.Handler(), r); err != nil {
								t.Fatalf("the bare JSON handler returned %v, want nil", err)
							}
							err := fanoutEmit(fan.Handler(), r)

							if console.String() != reference.String() {
								t.Errorf("console bytes differ from the pre-feature handler's\n fan-out: %q\nreference: %q",
									console.String(), reference.String())
							}

							// "Both wrote nothing" is a real answer for DEBUG and
							// below, and a vacuous pass for everything else, so
							// the reference side's emptiness is asserted rather
							// than assumed.
							wantOutput := r.Level >= slog.LevelInfo
							if got := reference.Len() > 0; got != wantOutput {
								t.Fatalf("the pre-feature handler wrote %d bytes at level %s, want any output = %t",
									reference.Len(), r.Level, wantOutput)
							}
							if wantOutput && strings.Count(console.String(), "\n") != 1 {
								t.Errorf("console wrote %q, want one newline-terminated line", console.String())
							}

							if verify != nil {
								verify(t, r, err)
							}
						})
					}
				})
			}
		})
	}
}

func TestFanoutOrGateDoesNotMakeTheConsoleChildEmitASuppressedRecord(t *testing.T) {
	// The mechanism that makes byte-identity possible at all. FanoutHandler.
	// Enabled is the OR across children, so a child with a lower threshold can
	// make the whole tree report enabled for a level the console child would
	// suppress — and Handle then has to ask each child's own Enabled before
	// handing over the record. If it did not, adding a Better Stack child would
	// start writing DEBUG to stdout, which is precisely the byte-identity
	// Req 2.5 protects.
	cases := []struct {
		name string
		// betterStackLevel is the Better Stack child's own threshold.
		betterStackLevel slog.Level
		// wantTreeEnabled is the fan-out's answer for DEBUG.
		wantTreeEnabled bool
		// wantBetterStackRecords is how many records reach the Better Stack
		// child, which is what makes the empty console a decision rather than
		// the fan-out declining the record outright.
		wantBetterStackRecords int
	}{
		{
			// Production today: neither child wants DEBUG, so slog never even
			// builds the record.
			name:             "Better Stack child at ERROR",
			betterStackLevel: slog.LevelError,
			wantTreeEnabled:  false,
		},
		{
			// The case that actually exercises the per-child gate: the OR says
			// yes, Handle runs, and the console child must still write nothing.
			name:                   "Better Stack child at DEBUG",
			betterStackLevel:       slog.LevelDebug,
			wantTreeEnabled:        true,
			wantBetterStackRecords: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var console bytes.Buffer
			betterStack := newFanoutChild(new(fanoutTrace), "shipper", tc.betterStackLevel)
			// The console child at its pre-feature configuration, whose options
			// default to LevelInfo.
			fan := NewFanoutHandler(slog.NewJSONHandler(&console, nil), betterStack)

			if got := fan.Enabled(context.Background(), slog.LevelDebug); got != tc.wantTreeEnabled {
				t.Errorf("Enabled(DEBUG) = %t, want %t", got, tc.wantTreeEnabled)
			}

			debug := newTestRecord(slog.LevelDebug, "not for stdout", "k", "v")
			if err := fanoutEmit(fan, debug); err != nil {
				t.Fatalf("Handle returned %v, want nil", err)
			}

			if console.Len() != 0 {
				t.Errorf("console wrote %q for a DEBUG record, want nothing: the OR gate leaked a suppressed record",
					console.String())
			}
			if got := len(betterStack.handled); got != tc.wantBetterStackRecords {
				t.Errorf("Better Stack child received %d records, want %d", got, tc.wantBetterStackRecords)
			}

			// And the console child is not simply disconnected: the same tree
			// writes an INFO record, so the silence above is the level gate.
			if err := fanoutEmit(fan, newTestRecord(slog.LevelInfo, "for stdout")); err != nil {
				t.Fatalf("Handle returned %v, want nil", err)
			}
			if console.Len() == 0 {
				t.Error("console wrote nothing for an INFO record, want the record: the buffer is not wired up")
			}
		})
	}
}
