package logging

// Two groups of tests for the Better_Stack_Handler.
//
// First, derivation semantics, asserted against the handler's internal state
// rather than against emitted output: the aliasing bug these tests exist to catch
// is invisible from the outside until two siblings both emit. Property 17
// (task 4.3) covers arbitrary WithAttrs/WithGroup interleavings end-to-end; what
// is here is the small set of examples that pin the contract itself — the two
// documented no-ops, the level gate in both directions, and the aliasing hazard.
//
// Second, the Handle path: the level gate's cheap exit, the admitted/dropped
// accounting, the convert -> redact -> cap -> enqueue ordering, and the recovered
// conversion panic. Those tests need a Shipper, so they use the scaffolding
// declared in handler.go; see newHandleTestShipper.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// derive folds a handler chain and asserts the result is still this package's
// Handler, so a test can read groups and frames.
func asHandler(t *testing.T, h slog.Handler) *Handler {
	t.Helper()
	typed, ok := h.(*Handler)
	if !ok {
		t.Fatalf("derived handler has type %T, want *logging.Handler", h)
	}
	return typed
}

// frameKeys renders a handler's frames as group path plus attr keys, which is
// the whole of what the derivation contract constrains.
func frameKeys(h *Handler) []string {
	rendered := make([]string, 0, len(h.frames))
	for _, frame := range h.frames {
		line := ""
		for _, g := range frame.groups {
			line += g + "/"
		}
		for i, a := range frame.attrs {
			if i > 0 {
				line += ","
			}
			line += a.Key
		}
		rendered = append(rendered, line)
	}
	return rendered
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHandlerWithAttrsEmptyBatchReturnsReceiver(t *testing.T) {
	h := NewHandler(nil, slog.LevelError)

	for name, attrs := range map[string][]slog.Attr{
		"nil batch":   nil,
		"empty batch": {},
	} {
		t.Run(name, func(t *testing.T) {
			if got := h.WithAttrs(attrs); got != slog.Handler(h) {
				t.Errorf("WithAttrs(%s) returned a new handler %p, want the receiver %p", name, got, h)
			}
		})
	}
}

func TestHandlerWithGroupEmptyNameReturnsReceiver(t *testing.T) {
	// Required by the slog.Handler contract, not merely permitted as it is for
	// an empty attr batch.
	h := NewHandler(nil, slog.LevelError)

	if got := h.WithGroup(""); got != slog.Handler(h) {
		t.Errorf(`WithGroup("") returned a new handler %p, want the receiver %p`, got, h)
	}
}

func TestHandlerEnabledIsTheThresholdComparison(t *testing.T) {
	// Req 2.9 and 2.10: below the threshold false, at or above it true. Nothing
	// else participates — this handler has no shipper at all, and still answers.
	h := NewHandler(nil, slog.LevelWarn)

	cases := []struct {
		level slog.Level
		want  bool
	}{
		{slog.LevelDebug, false},
		{slog.LevelInfo, false},
		{slog.LevelWarn - 1, false},
		{slog.LevelWarn, true},
		{slog.LevelError, true},
		{slog.LevelError + 4, true},
	}
	for _, tc := range cases {
		if got := h.Enabled(context.Background(), tc.level); got != tc.want {
			t.Errorf("Enabled(%s) = %t, want %t (threshold WARN)", tc.level, got, tc.want)
		}
	}
}

func TestHandlerEnabledFollowsADynamicLeveler(t *testing.T) {
	// slog.Leveler, not slog.Level, so a LevelVar retuned at runtime is read on
	// every call rather than captured at construction.
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelError)
	h := NewHandler(nil, levelVar)

	if h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Enabled(WARN) = true at an ERROR threshold, want false")
	}
	levelVar.Set(slog.LevelWarn)
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Enabled(WARN) = false after the LevelVar was lowered to WARN, want true")
	}
}

func TestHandlerNilLevelerDefaultsToError(t *testing.T) {
	// A nil Leveler must not panic on the first Enabled call (Req 8.8).
	h := NewHandler(nil, nil)

	if h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("Enabled(WARN) = true for a nil Leveler, want false (ERROR default)")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled(ERROR) = false for a nil Leveler, want true (ERROR default)")
	}
}

func TestHandlerAttrsBindToTheGroupsOpenWhenAdded(t *testing.T) {
	// h.WithAttrs(a).WithGroup("g").WithAttrs(b) must place a at context.a and b
	// at context.g.b. Storing a flat attr list and applying the currently open
	// groups at Handle time would put both under g.
	root := NewHandler(nil, slog.LevelError)
	derived := root.
		WithAttrs([]slog.Attr{slog.String("a", "1")}).
		WithGroup("g").
		WithAttrs([]slog.Attr{slog.String("b", "2")})

	h := asHandler(t, derived)
	want := []string{"a", "g/b"}
	if got := frameKeys(h); !equalStrings(got, want) {
		t.Errorf("frames = %v, want %v", got, want)
	}
	if got := h.groups; !equalStrings(got, []string{"g"}) {
		t.Errorf("groups = %v, want [g]", got)
	}
}

func TestHandlerSiblingsDerivedFromOneParentHaveDisjointFrames(t *testing.T) {
	// The aliasing hazard, set up directly: a parent whose frames and groups
	// slices carry spare capacity. Constructing it by hand is the point — the
	// clip discipline means no public derivation path can produce spare capacity,
	// so an internal parent is the only way to prove the discipline is what
	// prevents the bug rather than luck about capacities.
	parentFrames := make([]attrFrame, 0, 4)
	parentFrames = append(parentFrames, attrFrame{attrs: []slog.Attr{slog.String("base", "0")}})
	parentGroups := make([]string, 0, 4)
	parentGroups = append(parentGroups, "outer")

	parent := &Handler{level: slog.LevelError, frames: parentFrames, groups: parentGroups}

	left := asHandler(t, parent.WithAttrs([]slog.Attr{slog.String("left", "l")}))
	right := asHandler(t, parent.WithAttrs([]slog.Attr{slog.String("right", "r")}))

	if got, want := frameKeys(left), []string{"base", "outer/left"}; !equalStrings(got, want) {
		t.Errorf("left frames = %v, want %v (right's derivation clobbered them)", got, want)
	}
	if got, want := frameKeys(right), []string{"base", "outer/right"}; !equalStrings(got, want) {
		t.Errorf("right frames = %v, want %v", got, want)
	}
	if got, want := frameKeys(asHandler(t, slog.Handler(parent))), []string{"base"}; !equalStrings(got, want) {
		t.Errorf("parent frames = %v, want %v: a derivation wrote through to its parent", got, want)
	}

	// Same hazard on the group path, and on frames reached through WithGroup:
	// WithGroup appends to groups, and must clip frames too so a later WithAttrs
	// on either sibling cannot write into the other's array.
	inner := asHandler(t, parent.WithGroup("inner"))
	other := asHandler(t, parent.WithGroup("other"))
	if got, want := inner.groups, []string{"outer", "inner"}; !equalStrings(got, want) {
		t.Errorf("inner groups = %v, want %v", got, want)
	}
	if got, want := other.groups, []string{"outer", "other"}; !equalStrings(got, want) {
		t.Errorf("other groups = %v, want %v", got, want)
	}
	if got, want := parent.groups, []string{"outer"}; !equalStrings(got, want) {
		t.Errorf("parent groups = %v, want %v: WithGroup wrote through to its parent", got, want)
	}

	fromInner := asHandler(t, inner.WithAttrs([]slog.Attr{slog.String("i", "1")}))
	fromOther := asHandler(t, other.WithAttrs([]slog.Attr{slog.String("o", "1")}))
	if got, want := frameKeys(fromInner), []string{"base", "outer/inner/i"}; !equalStrings(got, want) {
		t.Errorf("frames after WithGroup+WithAttrs = %v, want %v", got, want)
	}
	if got, want := frameKeys(fromOther), []string{"base", "outer/other/o"}; !equalStrings(got, want) {
		t.Errorf("frames after WithGroup+WithAttrs = %v, want %v", got, want)
	}
}

func TestHandlerWithAttrsClonesTheCallerBatch(t *testing.T) {
	// slog hands WithAttrs a slice it is free to reuse, so a frame that kept the
	// caller's array would mutate under an already-derived handler.
	attrs := []slog.Attr{slog.String("k", "original")}
	h := asHandler(t, NewHandler(nil, slog.LevelError).WithAttrs(attrs))

	attrs[0] = slog.String("k", "mutated")

	if got := h.frames[0].attrs[0].Value.String(); got != "original" {
		t.Errorf("frame attr value = %q after the caller mutated its batch, want %q", got, "original")
	}
}

// ---------------------------------------------------------------------------
// Handle
// ---------------------------------------------------------------------------

// newHandleTestShipper builds the minimum Shipper the Handle path reads: a
// counters holder, a Redactor, a bounded Event_Queue, and a Self_Diagnostic
// logger writing into the returned buffer.
//
// It constructs the struct directly rather than calling a constructor because
// none exists yet (task 6.1). Every field it sets is one the real Shipper also
// has, under the same name, so this helper survives that replacement.
func newHandleTestShipper(t *testing.T, queueCap int) (*Shipper, *bytes.Buffer) {
	t.Helper()
	diagnostics := new(bytes.Buffer)
	return &Shipper{
		cfg: Config{
			ServiceName: testConvertConfig.ServiceName,
			Environment: testConvertConfig.Environment,
			SourceToken: "handle-test-token",
			DiagWriter:  diagnostics,
		},
		counters: &counters{},
		redactor: NewRedactor("handle-test-token"),
		queue:    make(chan *Event, queueCap),
		diag:     slog.New(slog.NewJSONHandler(diagnostics, nil)).With("logshipper", true),
	}, diagnostics
}

// queuedEvents drains the Event_Queue without blocking, so a test can assert both
// what was enqueued and that nothing else was.
func queuedEvents(s *Shipper) []*Event {
	events := make([]*Event, 0, len(s.queue))
	for {
		select {
		case ev := <-s.queue:
			events = append(events, ev)
		default:
			return events
		}
	}
}

// assertAccountingHolds checks the two counter identities Req 11.4 and Req 11.5
// state, plus the one part of the first identity that is not definitional: the
// derived queue depth must equal the number of events actually resident in the
// channel. QueueDepth is computed by subtraction, so a Handle path that counted
// an admission without either enqueueing or dropping shows up here and nowhere
// else.
func assertAccountingHolds(t *testing.T, s *Shipper) Snapshot {
	t.Helper()
	snap := s.counters.snapshot()

	if got, want := snap.Enqueued, snap.Delivered+snap.Dropped+snap.InFlight+snap.QueueDepth; got != want {
		t.Errorf("enqueued = %d, want delivered+dropped+in_flight+queue_depth = %d (%+v)", got, want, snap)
	}
	var byReason uint64
	for _, n := range snap.DroppedByReason {
		byReason += n
	}
	if byReason != snap.Dropped {
		t.Errorf("sum(dropped_by_reason) = %d, want dropped = %d (%+v)", byReason, snap.Dropped, snap)
	}
	if got, want := snap.QueueDepth, uint64(len(s.queue)); got != want {
		t.Errorf("derived queue_depth = %d, want len(queue) = %d (%+v)", got, want, snap)
	}
	return snap
}

// diagRecords decodes the Self_Diagnostics written during a test, one per line.
func diagRecords(t *testing.T, diagnostics *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(diagnostics.String()), "\n") {
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("Self_Diagnostic line is not JSON: %v (%q)", err, line)
		}
		records = append(records, fields)
	}
	return records
}

func TestHandleBelowThresholdDiscardsBeforeConverting(t *testing.T) {
	// Req 2.4: nil error, zero events added to the queue, and zero serialized
	// Log_Event bytes. The last of those is asserted the only way it can be
	// asserted from outside — by attaching a value whose MarshalJSON panics.
	// Conversion reaches it through json.Marshal, so any conversion or
	// serialization at all would raise a panic and leave a recorded drop, a
	// conversion-panic count, and a Self_Diagnostic behind. All three staying at
	// zero is what proves nothing was serialized.
	s, diagnostics := newHandleTestShipper(t, 4)
	h := NewHandler(s, slog.LevelError)

	record := newTestRecord(slog.LevelInfo, "below the threshold")
	record.AddAttrs(slog.Any("marshaler", p3PanicMarshaler{}))

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	if snap := s.counters.snapshot(); snap != (Snapshot{}) {
		t.Errorf("counters = %+v, want every counter still zero: nothing was admitted", snap)
	}
	if got := queuedEvents(s); len(got) != 0 {
		t.Errorf("queue holds %d events, want 0", len(got))
	}
	if diagnostics.Len() != 0 {
		t.Errorf("Self_Diagnostics = %q, want none", diagnostics.String())
	}
}

func TestHandleConvertsRedactsCapsAndEnqueuesExactlyOnce(t *testing.T) {
	// Req 2.3, 4.5, 4.7, 10.9, and the size cap's placement: one admission, one
	// enqueue, the sensitive value already replaced in the event that reached the
	// queue, and a cached serialized length matching the bytes the delivery
	// goroutine would write.
	s, diagnostics := newHandleTestShipper(t, 4)
	h := NewHandler(s, slog.LevelError)

	ctx := ContextWithUserID(
		ContextWithRequest(context.Background(), "req-7", "POST", "/v1/jobs"),
		"user-42",
	)
	record := newTestRecord(slog.LevelError, "job dispatch failed",
		"password", "hunter2", "job_id", "job-9")

	if err := h.Handle(ctx, record); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	snap := assertAccountingHolds(t, s)
	if snap.Enqueued != 1 || snap.Dropped != 0 || snap.ConversionPanics != 0 {
		t.Fatalf("counters = %+v, want exactly one admission and no drops", snap)
	}
	if diagnostics.Len() != 0 {
		t.Errorf("Self_Diagnostics = %q, want none for a clean record", diagnostics.String())
	}

	events := queuedEvents(s)
	if len(events) != 1 {
		t.Fatalf("queue holds %d events, want exactly 1", len(events))
	}
	ev := events[0]

	// Redaction ran, and it ran before the event became reachable from the queue
	// (Req 10.9): the queue never held the plaintext.
	if got := ev.Context["password"]; got != redactedPlaceholder {
		t.Errorf("context[password] = %#v, want %q", got, redactedPlaceholder)
	}
	if got := ev.Context["job_id"]; got != "job-9" {
		t.Errorf("context[job_id] = %#v, want %q", got, "job-9")
	}

	// Correlation came from the ctx argument, not from an attr (Req 4.5, 4.7).
	if ev.Request == nil || ev.Request.ID != "req-7" || ev.Request.Method != "POST" || ev.Request.Path != "/v1/jobs" {
		t.Errorf("request = %+v, want the triple carried by the context", ev.Request)
	}
	if ev.User == nil || ev.User.ID != "user-42" {
		t.Errorf("user = %+v, want the id carried by the context", ev.User)
	}

	// The size cap ran last, so the cached length is the event's exact
	// contribution to a Delivery_Batch body.
	encoded, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("enqueued event does not marshal: %v", err)
	}
	if got := ev.SerializedLen(); got != len(encoded) {
		t.Errorf("SerializedLen() = %d, want %d: the size cap did not run on the Handle path", got, len(encoded))
	}
}

func TestHandleOmitsRequestAndUserWhenTheContextCarriesNeither(t *testing.T) {
	// Req 4.10: a record emitted through slog.Error carries context.Background(),
	// so both lookups miss. The event is still admitted and still enqueued — the
	// absence is not an error and not a reason to discard.
	s, _ := newHandleTestShipper(t, 4)
	h := NewHandler(s, slog.LevelError)

	if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "no correlation")); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	events := queuedEvents(s)
	if len(events) != 1 {
		t.Fatalf("queue holds %d events, want exactly 1", len(events))
	}
	if events[0].Request != nil {
		t.Errorf("request = %+v, want it omitted", events[0].Request)
	}
	if events[0].User != nil {
		t.Errorf("user = %+v, want it omitted", events[0].User)
	}
}

func TestHandleRecoversConversionPanicAndLeavesTheQueueUntouched(t *testing.T) {
	// Req 8.5 and 8.8, for the two hazards whose panic genuinely escapes
	// conversion: MarshalJSON, reached through json.Marshal, and Error, called
	// directly by errorInfo. A panicking String is absorbed by fmt and a
	// panicking LogValue by slog.Value.Resolve, so neither reaches here as a
	// panic; see prop_p3_hostile_test.go.
	cases := []struct {
		name string
		attr slog.Attr
		want string
	}{
		{"MarshalJSON", slog.Any("marshaler", p3PanicMarshaler{}), p3MarshalJSONPanic},
		{"Error", slog.Any("err", p3PanicError{}), p3ErrorPanic},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, diagnostics := newHandleTestShipper(t, 4)
			h := NewHandler(s, slog.LevelError)

			// One previously enqueued event, so "the Event_Queue length and the
			// contents of every previously enqueued Log_Event unchanged" is a
			// claim about something rather than about an empty channel.
			if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "first", "n", 1)); err != nil {
				t.Fatalf("Handle returned %v for the clean record, want nil", err)
			}
			before := s.counters.snapshot()

			hostile := newTestRecord(slog.LevelError, "hostile")
			hostile.AddAttrs(tc.attr)

			// Reaching the next line is half the assertion: the panic did not
			// propagate into this goroutine (Req 8.8).
			if err := h.Handle(context.Background(), hostile); err != nil {
				t.Fatalf("Handle returned %v for the hostile record, want nil", err)
			}

			after := assertAccountingHolds(t, s)
			if got, want := after.Enqueued, before.Enqueued+1; got != want {
				t.Errorf("enqueued = %d, want %d: the admission must be counted before conversion", got, want)
			}
			if got, want := after.Dropped, before.Dropped+1; got != want {
				t.Errorf("dropped = %d, want %d", got, want)
			}
			if got, want := after.DroppedByReason[reasonConversionPanic],
				before.DroppedByReason[reasonConversionPanic]+1; got != want {
				t.Errorf("dropped[reasonConversionPanic] = %d, want %d", got, want)
			}
			if got, want := after.ConversionPanics, before.ConversionPanics+1; got != want {
				t.Errorf("conversion_panics = %d, want %d", got, want)
			}

			// The queue is untouched: still one event, still the clean one, with
			// its context intact.
			events := queuedEvents(s)
			if len(events) != 1 {
				t.Fatalf("queue holds %d events, want the 1 enqueued before the panic", len(events))
			}
			if got := events[0].Message; got != "first" {
				t.Errorf("queued message = %q, want %q", got, "first")
			}
			if got := events[0].Context["n"]; got != int64(1) {
				t.Errorf("queued context[n] = %#v, want int64(1): a previously enqueued event was modified", got)
			}

			// Exactly one Self_Diagnostic, written through the logshipper-marked
			// logger rather than through the fan-out handler (Req 8.1, 8.2).
			records := diagRecords(t, diagnostics)
			if len(records) != 1 {
				t.Fatalf("wrote %d Self_Diagnostics, want exactly 1: %q", len(records), diagnostics.String())
			}
			diag := records[0]
			if diag["logshipper"] != true {
				t.Errorf("Self_Diagnostic = %v, want the logshipper marker attribute", diag)
			}
			if diag["level"] != slog.LevelError.String() {
				t.Errorf("Self_Diagnostic level = %v, want %s", diag["level"], slog.LevelError)
			}
			if got, _ := diag["panic"].(string); got != tc.want {
				t.Errorf("Self_Diagnostic panic = %#v, want the recovered value %q", diag["panic"], tc.want)
			}
			if got, _ := diag["stack"].(string); !strings.Contains(got, "logging.(*Handler).Handle") {
				t.Errorf("Self_Diagnostic stack = %q, want the panicking goroutine's stack", got)
			}

			// And the handler still works: a clean record after the panic is
			// admitted and enqueued normally.
			if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "after")); err != nil {
				t.Fatalf("Handle returned %v after the recovered panic, want nil", err)
			}
			if got := queuedEvents(s); len(got) != 1 || got[0].Message != "after" {
				t.Errorf("queue after the panic holds %d events, want the 1 clean record that followed", len(got))
			}
			// The identity is not re-asserted here: the drains above removed
			// events from the channel without counting a delivery, which the
			// derived queue depth has no way to know about. The totals are what
			// remains checkable — three admissions, one drop.
			if final := s.counters.snapshot(); final.Enqueued != before.Enqueued+2 || final.Dropped != 1 {
				t.Errorf("counters = %+v, want 3 admissions and exactly 1 drop", final)
			}
		})
	}
}

// handleLongPanicMarshaler panics with 2000 multi-byte characters, so the
// diagnostic's cut is exercised on a value that is both over the character cap
// and impossible to cut mid-rune without producing invalid UTF-8.
type handleLongPanicMarshaler struct{}

func (handleLongPanicMarshaler) MarshalJSON() ([]byte, error) {
	panic(strings.Repeat("é", 2000))
}

func TestHandlePanicDiagnosticTruncatesTheValueAndTheStack(t *testing.T) {
	// Req 8.3's cuts, applied to the handler's recovery: the panic value at 1024
	// characters and the stack at 8192 bytes.
	s, diagnostics := newHandleTestShipper(t, 4)
	h := NewHandler(s, slog.LevelError)

	record := newTestRecord(slog.LevelError, "hostile")
	record.AddAttrs(slog.Any("marshaler", handleLongPanicMarshaler{}))

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	records := diagRecords(t, diagnostics)
	if len(records) != 1 {
		t.Fatalf("wrote %d Self_Diagnostics, want exactly 1", len(records))
	}
	panicText, _ := records[0]["panic"].(string)
	if got := utf8.RuneCountInString(panicText); got != maxDiagPanicChars {
		t.Errorf("panic value holds %d characters, want it cut to %d", got, maxDiagPanicChars)
	}
	if !utf8.ValidString(panicText) {
		t.Errorf("panic value is not valid UTF-8: the cut landed inside a rune")
	}
	stack, _ := records[0]["stack"].(string)
	if len(stack) > maxDiagStackBytes {
		t.Errorf("stack holds %d bytes, want at most %d", len(stack), maxDiagStackBytes)
	}
	if stack == "" {
		t.Error("stack is empty, want the panicking goroutine's stack")
	}
}

func TestHandleNilShipperDiscardsWithoutErrorOrPanic(t *testing.T) {
	// Req 12.9: the conversion-only handler. The hostile attr is what makes this
	// a real assertion — a nil shipper that converted anyway would panic here
	// with no counters to recover into.
	h := NewHandler(nil, slog.LevelError)

	record := newTestRecord(slog.LevelError, "no shipper")
	record.AddAttrs(slog.Any("marshaler", p3PanicMarshaler{}))

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}
	if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "clean")); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}
}

func TestHandleDerivedHandlerShipsFrameAttrsUnderTheirOwnGroups(t *testing.T) {
	// The derivation contract, observed through Handle rather than through the
	// handler's internal state: h.WithAttrs(a).WithGroup("g").WithAttrs(b) places
	// a at context.a and b at context.g.b, and the record's own attrs nest under
	// the groups open at Handle time (Req 3.9, 3.10).
	s, _ := newHandleTestShipper(t, 4)
	h := NewHandler(s, slog.LevelError).
		WithAttrs([]slog.Attr{slog.String("a", "1")}).
		WithGroup("g").
		WithAttrs([]slog.Attr{slog.String("b", "2")})

	if err := h.Handle(context.Background(), newTestRecord(slog.LevelError, "derived", "c", "3")); err != nil {
		t.Fatalf("Handle returned %v, want nil", err)
	}

	events := queuedEvents(s)
	if len(events) != 1 {
		t.Fatalf("queue holds %d events, want exactly 1", len(events))
	}
	root := events[0].Context
	if got := root["a"]; got != "1" {
		t.Errorf("context[a] = %#v, want %q: the first batch was added before any group opened", got, "1")
	}
	group, _ := root["g"].(map[string]any)
	if group == nil {
		t.Fatalf("context = %v, want a `g` object holding the second batch and the record attr", root)
	}
	if got := group["b"]; got != "2" {
		t.Errorf("context.g[b] = %#v, want %q", got, "2")
	}
	if got := group["c"]; got != "3" {
		t.Errorf("context.g[c] = %#v, want %q: record attrs nest under the groups open at Handle time", got, "3")
	}
}
