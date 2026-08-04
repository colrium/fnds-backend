package logging

// Property 3: hostile-input safety.
//
// This file holds Property 3 only. It lives apart from event_test.go and
// convert_test.go so that the two conversion invariants — "the payload says what
// the record said" (Property 2) and "nothing the call site can do turns a log
// line into a crash" (Property 3) — can be read independently.
//
// The property spans two layers, and only one of them exists yet:
//
//  1. **Conversion (this task).** convertRecord deliberately does not recover
//     panics; see the header of convert.go. So the strongest claim that can be
//     stated about conversion alone is a disjunction: for any Log_Record,
//     convertRecord either returns a Log_Event that marshals to valid UTF-8 JSON
//     carrying every required top-level field, or it panics. Every assertion in
//     this file guards the first branch, and pins down which hazards may take
//     the second branch at all.
//
//  2. **Containment.** "Handle returns nil and propagates zero panics"
//     (Req 8.5, 8.8) is Handler.Handle's obligation.
//     TestProperty3_PanicContainmentIsRecoverableByTheCaller asserts the layer
//     underneath it — the escaping panic is an ordinary, recoverable Go panic,
//     not a fatal runtime error, so a recover in the caller's frame contains it
//     and control returns to the caller — through p3Convert, a two-line stand-in
//     for Handle's recover. That claim is kept here rather than folded into the
//     handler tests because it is a property of *conversion*: it fails loudly if
//     conversion ever becomes uncontainable, whatever Handle does.
//     Handle's own half of the claim — the nil return, the
//     dropped/conversion-panic counters, the single Self_Diagnostic, and the
//     untouched Event_Queue — is asserted against the real Handle in
//     handler_test.go, by TestHandleRecoversConversionPanicAndLeavesTheQueueUntouched.
//
// A finding worth recording, because it decides the expectations in the table
// below. Of the hazards Req 3.13 and Property 3 name, the two "panicking method"
// cases never reach convertRecord as panics at all:
//
//   - A panicking String is invoked by fmt, from formatUnrepresentable, and fmt
//     recovers panics raised by String and Error and renders them inline as
//     %!v(PANIC=String method: ...).
//   - A panicking LogValue is invoked by slog.Value.Resolve, which recovers and
//     substitutes an error value reading "LogValue panicked" followed by a stack
//     excerpt.
//
// Both are therefore asserted here as *non*-panicking, valid-JSON outcomes. A
// panic that genuinely escapes conversion has to come from the other two methods
// convert.go's header names — MarshalJSON, reached through normalizeAny's
// json.Marshal, and Error, called directly by errorInfo — so those two are
// declared locally (p3PanicMarshaler, p3PanicError) to give the containment test
// a real panic to contain. Neither is a production change; both are test types.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Guarded conversion
// ---------------------------------------------------------------------------

// p3Outcome is the result of one guarded conversion: either an Event, or the
// value a panic carried. It exists so a property can treat "panicked" as data
// rather than as a test failure, which is what lets the disjunction above be
// asserted.
type p3Outcome struct {
	event     *Event
	panicked  bool
	recovered any
}

// p3Convert converts one Log_Record with a recover in the *caller's* frame.
//
// It mirrors Handler.Handle's recover without needing a Shipper, which is what
// lets this file state its property over conversion alone: a panic raised inside
// conversion is an ordinary Go panic value, so a deferred recover one frame up
// stops it and execution continues normally afterwards. It also keeps the
// panicked/not-panicked outcome visible as data, which a direct Handle call
// cannot do — Handle contains the panic and returns nil.
//
// The assertions that need a Shipper — Handle's nil return, the dropped-event
// and conversion-panic counters, the unchanged Event_Queue, and the single
// Self_Diagnostic — live in handler_test.go against the real Handle.
func p3Convert(r slog.Record) (out p3Outcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out.panicked = true
			out.recovered = recovered
		}
	}()

	out.event = convertRecord(context.Background(), r, testConvertConfig, nil, nil)
	return out
}

// p3RequiredFields are the six top-level fields Req 3.1 makes unconditional. A
// hostile attribute value must not be able to cost any of them — an event that
// lost `dt` or `truncated` on the way through a channel-valued attr would be
// unusable in the Better Stack UI even though it parsed.
var p3RequiredFields = []string{"dt", "level", "message", "service", "environment", "truncated"}

// p3EventJSON is the invariant half of the property: it marshals ev, checks that
// the bytes are valid UTF-8 encoded JSON (Req 3.13), and returns the decoded
// object after confirming every required top-level field survived.
//
// It reports an error rather than failing the test, so the randomized loop can
// attach the generated counterexample through pbtFailf instead of losing it to a
// bare t.Errorf inside a helper.
func p3EventJSON(ev *Event) (map[string]any, error) {
	if ev == nil {
		return nil, errors.New("conversion returned a nil Event")
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("json.Marshal(Event) returned error: %w", err)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("serialized Event is not valid JSON: %q", raw)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("serialized Event is not valid UTF-8: %q", raw)
	}

	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("serialized Event does not decode as a JSON object: %w (%s)", err, raw)
	}
	for _, key := range p3RequiredFields {
		if _, present := fields[key]; !present {
			return nil, fmt.Errorf("required top-level field %q missing from %s", key, raw)
		}
	}
	return fields, nil
}

// p3ContextOf returns the decoded `context` object, or nil when the event
// carries none. A nil result is a legitimate outcome here: an attr dropped by
// the depth cap, or a zero Attr, leaves `context` empty and omitempty removes it.
func p3ContextOf(fields map[string]any) map[string]any {
	object, _ := fields["context"].(map[string]any)
	return object
}

// ---------------------------------------------------------------------------
// Hostile types whose panic actually escapes conversion
// ---------------------------------------------------------------------------

const (
	// p3MarshalJSONPanic escapes through normalizeAny's json.Marshal.
	p3MarshalJSONPanic = "p3: MarshalJSON panicked"
	// p3ErrorPanic escapes through errorInfo's direct err.Error() call.
	p3ErrorPanic = "p3: Error panicked"
)

// p3PanicMarshaler panics from MarshalJSON. encoding/json recovers only its own
// internal error sentinel and re-panics everything else, so this reaches the
// caller of convertRecord.
type p3PanicMarshaler struct{}

func (p3PanicMarshaler) MarshalJSON() ([]byte, error) { panic(p3MarshalJSONPanic) }

// p3PanicError panics from Error. errorInfo calls err.Error() directly rather
// than through fmt, so no library recover stands in the way.
type p3PanicError struct{}

func (p3PanicError) Error() string { panic(p3ErrorPanic) }

// p3IsHostileMethodPanic reports whether a recovered value came from a
// deliberately hostile test method. Every such method panics with a string
// prefixed "pbt: " (the harness types) or "p3: " (the two above), so a panic
// failing this check is a genuine conversion bug — a nil map write, an index out
// of range — and must fail the property rather than be tolerated as "hostile
// input is allowed to panic".
func p3IsHostileMethodPanic(recovered any) bool {
	text, ok := recovered.(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(text, "pbt: ") || strings.HasPrefix(text, "p3: ")
}

// ---------------------------------------------------------------------------
// Randomized property
// ---------------------------------------------------------------------------

// Feature: betterstack-error-logging, Property 3: Every Log_Record produces valid JSON and never panics the caller
// **Validates: Requirements 3.13, 8.5, 8.8**
//
// For any Log_Record drawn with every hazard enabled — nil and typed-nil values,
// invalid UTF-8 in messages, keys, and values, group nesting on both sides of the
// depth cap, channel, func and complex values, self-referential maps and slices,
// and values whose String or LogValue method panics — conversion either yields a
// Log_Event that marshals to valid UTF-8 JSON carrying all six required
// top-level fields, or panics with a value raised by the hostile method itself.
func TestProperty3_HostileRecordsConvertToValidJSONOrPanicOnlyFromHostileMethods(t *testing.T) {
	r, seed := pbtRand(t)

	converted, panicked := 0, 0
	for i := 0; i < pbtIterations; i++ {
		record := genRecord(r) // defaults to hazardsAll

		out := p3Convert(record)
		if out.panicked {
			panicked++
			// The disjunction's second branch is permitted, but only for a panic
			// the generated value raised. Anything else is a conversion bug.
			if !p3IsHostileMethodPanic(out.recovered) {
				pbtFailf(t, seed, i, record,
					"conversion panicked with a value no hostile test method raised: %#v (%T)",
					out.recovered, out.recovered)
			}
			continue
		}

		converted++
		if _, err := p3EventJSON(out.event); err != nil {
			pbtFailf(t, seed, i, record, "%v", err)
		}
	}

	t.Logf("%d/%d records converted, %d panicked", converted, pbtIterations, panicked)

	// A run in which nothing converted would satisfy the disjunction vacuously.
	if converted == 0 {
		pbtFatalf(t, seed, pbtIterations, nil,
			"every one of %d generated records panicked; the property held vacuously", pbtIterations)
	}
}

// Feature: betterstack-error-logging, Property 3: Every Log_Record produces valid JSON and never panics the caller
// **Validates: Requirements 3.13, 8.5, 8.8**
//
// The same property over a deterministic enumeration of every hazard class
// Req 3.13 and Property 3 name, so the property cannot pass because 100 random
// draws happened to miss a category — and so the *specific* outcome of each
// class is pinned down rather than merely "it did not crash".
func TestProperty3_EveryEnumeratedHazardClassIsHandled(t *testing.T) {
	for _, hazard := range p3Hazards() {
		t.Run(hazard.name, func(t *testing.T) {
			record := newTestRecord(slog.LevelError, "hostile \xff message")
			record.AddAttrs(hazard.attr)

			out := p3Convert(record)

			if hazard.panics {
				if !out.panicked {
					t.Fatalf("conversion returned normally; this hazard's panic must reach the caller "+
						"so Handle can recover it and account for the drop (Req 8.5, 8.8): event = %+v", out.event)
				}
				if !p3IsHostileMethodPanic(out.recovered) {
					t.Fatalf("recovered %#v (%T), want the panic raised by the hostile method itself",
						out.recovered, out.recovered)
				}
				return
			}

			if out.panicked {
				t.Fatalf("conversion panicked with %#v; this hazard must be handled cleanly, not by panic",
					out.recovered)
			}

			fields, err := p3EventJSON(out.event)
			if err != nil {
				t.Fatalf("%v", err)
			}
			// Req 3.13: the invalid bytes in the message are replaced, never
			// passed through, for every hazard class.
			if strings.Contains(out.event.Message, "\xff") {
				t.Errorf("Message = %q, want the invalid byte sequence replaced", out.event.Message)
			}
			if hazard.check != nil {
				hazard.check(t, out.event, p3ContextOf(fields))
			}
		})
	}
}

// p3Hazard is one enumerated hazard class: the attribute that carries it, and
// what conversion must do with it.
type p3Hazard struct {
	name string
	attr slog.Attr

	// panics records that this hazard's panic is *expected* to reach the caller.
	// It is not a tolerance: convert.go's header states that such a panic
	// propagates to Handle on purpose, so that Handle counts one dropped event
	// and one recovered conversion panic (Req 8.5, 8.8). Recovering inside
	// conversion would hide that accounting, so the propagation is asserted.
	panics bool

	// check pins down the handled outcome. ctxObj is nil when the event carries
	// no `context` object, which the depth cap and the zero Attr both produce.
	check func(t *testing.T, ev *Event, ctxObj map[string]any)
}

func p3Hazards() []p3Hazard {
	// Group nesting two levels past maxContextDepth, so the leaf is dropped and
	// `truncated` latches instead of the walk recursing without bound.
	deepGroups := slog.Attr{Key: "leaf", Value: slog.StringValue("bottom")}
	for level := 0; level < 12; level++ {
		deepGroups = slog.Attr{Key: "g", Value: slog.GroupValue(deepGroups)}
	}

	return []p3Hazard{
		{
			name: "nil_value_becomes_json_null",
			attr: slog.Any("nil_value", nil),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantJSONNull(t, ctxObj, "nil_value")
			},
		},
		{
			name: "typed_nil_pointer_becomes_json_null",
			attr: slog.Any("typed_nil", (*pbtScalarStruct)(nil)),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantJSONNull(t, ctxObj, "typed_nil")
			},
		},
		{
			// One replacement character per contiguous invalid *sequence*, which
			// is Req 3.13's wording and strings.ToValidUTF8's behaviour: the
			// two-byte run \xff\xfe collapses to one U+FFFD, while the two
			// separated bytes later in the string produce one each.
			name: "invalid_utf8_value_becomes_replacement_character",
			attr: slog.String("invalid_value", "run \xff\xfe then \xffa\xffb"),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				bad := string(utf8.RuneError)
				want := "run " + bad + " then " + bad + "a" + bad + "b"
				if got := ctxObj["invalid_value"]; got != want {
					t.Errorf("context[invalid_value] = %#v, want %q", got, want)
				}
			},
		},
		{
			name: "invalid_utf8_key_becomes_replacement_character",
			attr: slog.String("invalid\xffkey", "kept"),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				key := "invalid" + string(utf8.RuneError) + "key"
				if got, present := ctxObj[key]; !present || got != "kept" {
					t.Errorf("context = %v, want the sanitized key %q holding %q", ctxObj, key, "kept")
				}
			},
		},
		{
			name: "channel_value_falls_back_to_go_formatting",
			attr: slog.Any("channel", make(chan int)),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantString(t, ctxObj, "channel")
			},
		},
		{
			name: "func_value_falls_back_to_go_formatting",
			attr: slog.Any("func", func() {}),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantString(t, ctxObj, "func")
			},
		},
		{
			name: "complex_value_falls_back_to_go_formatting",
			attr: slog.Any("complex", complex(1, 2)),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				if got := p3WantString(t, ctxObj, "complex"); got != "" && !strings.Contains(got, "2i") {
					t.Errorf("context[complex] = %q, want Go's complex formatting", got)
				}
			},
		},
		{
			name: "struct_holding_a_channel_falls_back_to_go_formatting",
			attr: slog.Any("chan_struct", pbtChanStruct{Ch: make(chan int)}),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantString(t, ctxObj, "chan_struct")
			},
		},
		{
			// fmt applies no cycle detection to maps and slices, and a stack
			// overflow is a fatal runtime error that no recover — not Handle's,
			// not p3Convert's — can catch. A self-referential container is
			// therefore described by type rather than formatted.
			name: "self_referential_map_is_described_not_formatted",
			attr: slog.Any("cyclic_map", pbtCyclicMap()),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantUnrepresentable(t, ctxObj, "cyclic_map")
			},
		},
		{
			name: "self_referential_slice_is_described_not_formatted",
			attr: slog.Any("cyclic_slice", pbtCyclicSlice()),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				p3WantUnrepresentable(t, ctxObj, "cyclic_slice")
			},
		},
		{
			name: "nan_and_infinity_become_text",
			attr: slog.Float64("nan", math.NaN()),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				if got := ctxObj["nan"]; got != "NaN" {
					t.Errorf("context[nan] = %#v, want %q, since JSON has no NaN literal", got, "NaN")
				}
			},
		},
		{
			name: "negative_infinity_becomes_text",
			attr: slog.Float64("inf", math.Inf(-1)),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				if got := ctxObj["inf"]; got != "-Inf" {
					t.Errorf("context[inf] = %#v, want %q", got, "-Inf")
				}
			},
		},
		{
			// The hazard Property 3 names as "values whose String method
			// panics". It does not reach conversion as a panic: the String call
			// happens inside fmt, from formatUnrepresentable, and fmt recovers
			// panics from String and renders them inline.
			name: "panicking_string_method_is_absorbed_by_fmt",
			attr: slog.Any("panic_stringer", pbtPanicStringer{Ch: make(chan int)}),
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				got := p3WantString(t, ctxObj, "panic_stringer")
				if got != "" && !strings.Contains(got, "PANIC") {
					t.Errorf("context[panic_stringer] = %q, want fmt's inline panic rendering", got)
				}
			},
		},
		{
			// The hazard Property 3 names as "values whose LogValue method
			// panics". slog.Value.Resolve recovers it and substitutes an error
			// value, which appendAttrs then lifts into the top-level `error`
			// object like any other error-valued attr (Req 3.6).
			name: "panicking_logvalue_method_is_absorbed_by_resolve",
			attr: slog.Any("panic_logvaluer", pbtPanicLogValuer{}),
			check: func(t *testing.T, ev *Event, _ map[string]any) {
				if ev.Error == nil {
					t.Fatalf("event carries no error object; Resolve's substituted error value was lost")
				}
				if !strings.Contains(ev.Error.Message, "LogValue panicked") {
					t.Errorf("error.message = %q, want Resolve's substituted description",
						ev.Error.Message)
				}
			},
		},
		{
			// An error that unwraps to itself. Only the 10-entry chain cap
			// terminates the walk, and hitting it latches `truncated` (Req 3.7,
			// 3.14).
			name: "self_unwrapping_error_terminates_at_the_chain_cap",
			attr: slog.Any("err", pbtSelfError{}),
			check: func(t *testing.T, ev *Event, _ map[string]any) {
				if ev.Error == nil {
					t.Fatalf("event carries no error object for a self-unwrapping error")
				}
				if got := len(ev.Error.Chain); got != maxErrorChainLen {
					t.Errorf("len(error.chain) = %d, want the cap %d", got, maxErrorChainLen)
				}
				if !ev.Truncated {
					t.Errorf("truncated = false, want true after the error chain was capped")
				}
			},
		},
		{
			name: "group_nesting_past_the_depth_cap_is_dropped_not_recursed",
			attr: deepGroups,
			check: func(t *testing.T, ev *Event, ctxObj map[string]any) {
				if !ev.Truncated {
					t.Errorf("truncated = false, want true after the depth cap dropped the leaf")
				}
				if len(ctxObj) != 0 {
					t.Errorf("context = %v, want it empty: every level was past the cap", ctxObj)
				}
			},
		},
		{
			name: "zero_attr_is_skipped",
			attr: slog.Attr{},
			check: func(t *testing.T, _ *Event, ctxObj map[string]any) {
				if len(ctxObj) != 0 {
					t.Errorf("context = %v, want no context object at all for the zero Attr", ctxObj)
				}
			},
		},
		{
			// The two hazards whose panic genuinely escapes conversion, and so
			// the two that make Handle's recover load-bearing.
			name:   "panicking_marshaljson_propagates_to_the_caller",
			attr:   slog.Any("marshaler", p3PanicMarshaler{}),
			panics: true,
		},
		{
			name:   "panicking_error_method_propagates_to_the_caller",
			attr:   slog.Any("err", p3PanicError{}),
			panics: true,
		},
	}
}

// p3WantJSONNull asserts a key is present and decoded as JSON null (Req 3.13).
// Presence matters as much as the value: a dropped key and a null one are
// different payloads.
func p3WantJSONNull(t *testing.T, ctxObj map[string]any, key string) {
	t.Helper()
	value, present := ctxObj[key]
	if !present || value != nil {
		t.Errorf("context[%s] = %#v (present=%v), want JSON null", key, value, present)
	}
}

// p3WantString asserts a value fell back to Go string formatting (Req 3.13) and
// returns it, so a caller can make a sharper claim about its content.
func p3WantString(t *testing.T, ctxObj map[string]any, key string) string {
	t.Helper()
	text, ok := ctxObj[key].(string)
	if !ok {
		t.Errorf("context[%s] = %#v, want default Go string formatting", key, ctxObj[key])
		return ""
	}
	return text
}

// p3WantUnrepresentable asserts a value was described by type rather than
// formatted, which is how convert.go keeps a reference cycle from becoming an
// unrecoverable stack overflow.
func p3WantUnrepresentable(t *testing.T, ctxObj map[string]any, key string) {
	t.Helper()
	if text := p3WantString(t, ctxObj, key); text != "" && !strings.HasPrefix(text, "<unrepresentable ") {
		t.Errorf("context[%s] = %q, want an <unrepresentable T> description", key, text)
	}
}

// ---------------------------------------------------------------------------
// Containment — Handle's half of the property (task 4.2)
// ---------------------------------------------------------------------------

// Feature: betterstack-error-logging, Property 3: Every Log_Record produces valid JSON and never panics the caller
// **Validates: Requirements 3.13, 8.5, 8.8**
//
// The containment half of the property. Req 8.5 and 8.8 assign it to
// Handler.Handle; what is asserted here is the precondition Handle depends on
// and that belongs to conversion: a panic raised during conversion is an
// ordinary Go panic that a deferred recover one frame up catches, after which
// the goroutine keeps running and conversion still works. That is what makes
// "Handle returns nil and propagates zero panics" implementable at all — and it
// is a real claim, because the one failure mode a recover cannot catch, a stack
// overflow from formatting a reference cycle, is prevented structurally rather
// than recovered (see the cyclic cases in p3Hazards).
//
// The three assertions that need a Shipper — Handle returned nil, the
// dropped-event and conversion-panic counters each advanced by exactly one with
// one Self_Diagnostic written, and the Event_Queue length and previously enqueued
// events are unchanged — are made in handler_test.go by
// TestHandleRecoversConversionPanicAndLeavesTheQueueUntouched.
func TestProperty3_PanicContainmentIsRecoverableByTheCaller(t *testing.T) {
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
			record := newTestRecord(slog.LevelError, "hostile")
			record.AddAttrs(tc.attr)

			// Reaching the next line at all is the assertion: the panic was
			// recoverable, so control returned to this goroutine.
			out := p3Convert(record)
			if !out.panicked {
				t.Fatalf("conversion returned normally, so there is no panic for Handle to contain")
			}
			if got := out.recovered; got != tc.want {
				t.Errorf("recovered %#v (%T), want %q", got, got, tc.want)
			}

			// And the process is unharmed: a subsequent clean record still
			// converts to a complete Log_Event. This is the assertion that
			// becomes "the Event_Queue and every previously enqueued Log_Event
			// are unchanged" once Handle exists (Req 8.5).
			after := p3Convert(newTestRecord(slog.LevelError, "after the panic", "ok", true))
			if after.panicked {
				t.Fatalf("a clean record panicked after the hostile one: %#v", after.recovered)
			}
			fields, err := p3EventJSON(after.event)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if got := p3ContextOf(fields)["ok"]; got != true {
				t.Errorf("context[ok] = %#v, want true: the clean record converted incompletely", got)
			}
		})
	}
}
