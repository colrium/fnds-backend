package logging

// Feature: betterstack-error-logging — Property 4: Redaction is idempotent.
//
// *For any* Log_Event, `redact(redact(event))` equals `redact(event)`.
//
// **Validates: Requirements 10.1, 10.2, 10.3**
//
// Why the property holds, and therefore what a counterexample would mean: every
// decision Redactor.Apply makes is a function of the key path and of the value's
// equality with the Source_Token, and both replacement strings are fixed points
// of those rules. A key that matched [REDACTED] on the first pass still matches
// on the second and is replaced with the same string, and a subtree the depth
// cap collapsed to [TRUNCATED] has nothing left to descend into. A failure here
// therefore means some decision has become a function of state that the first
// pass changed — a content-shape heuristic, a placeholder that is not its own
// fixed point, or a `truncated` flag that got cleared rather than latched.
//
// This file holds only Property 4. Redaction's example-based cases live in
// redact_test.go and the structure-preservation property in its own file, so
// three concurrently developed tests never contend for one file.

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
)

// p4Tokens are Source_Token values shaped like the real one. The empty token is
// generated separately, in p4Token, because it is not one choice among several
// but a distinct mode: an empty token disables the equality rule entirely, and
// it is the normal state in tests and local development (Req 12.3). Both modes
// must satisfy idempotence, and only the non-empty mode makes the equality rule
// fire at all.
var p4Tokens = []string{
	"st_live_abcdef123456",
	"tok",
	"HYtLZ4qGRnMkPqQ2mE8w9SxA",
}

func TestProperty4RedactionIsIdempotent(t *testing.T) {
	r, seed := pbtRand(t)

	// Coverage counters. Idempotence is trivially satisfiable by a redactor
	// that does nothing, so a run in which nothing was ever redacted proves
	// nothing. These are asserted after the loop.
	redactedSomething := 0
	truncatedSomething := 0

	for i := 0; i < pbtIterations; i++ {
		// Most draws use the serializable default; the rest enable every
		// hazard, which is the only way to generate a cyclic `context` (the
		// harness documents genEventWith(r, hazardsAll) as the way to ask for
		// cycles). Idempotence must hold for a cyclic event too: the depth cap
		// is what terminates the walk, and its [TRUNCATED] replacement has to
		// be stable under a second pass.
		var generated *Event
		if pbtChance(r, 30) {
			generated = genEventWith(r, hazardsAll)
		} else {
			generated = genEvent(r)
		}

		// The token is chosen after generation so it can be planted as a value
		// inside the event, which is what makes the Source_Token equality rule
		// (Req 10.11) actually fire rather than being sampled at probability
		// zero against random words.
		token := p4Token(r, generated)
		redactor := NewRedactor(token)

		// Two independent deep copies of the same generated event. Apply
		// mutates in place — it replaces ev.Context wholesale and writes into
		// ErrorInfo.Chain element by element — so comparing one application
		// against two requires two separate events that start out equal.
		//
		// The copies are *deep*, and deliberately so even though redactValue
		// happens to build fresh maps rather than mutating its input today. If
		// the two events shared their nested `context` maps, a future change
		// that made redaction mutate in place would have the second pass edit
		// the very tree the first pass produced, both events would end up
		// pointing at the same objects, and this property would pass
		// vacuously — precisely in the case where it should fail.
		once := p4CopyEvent(generated)
		twice := p4CopyEvent(generated)

		redactor.Apply(once)
		redactor.Apply(twice)
		redactor.Apply(twice)

		// Comparison runs on the *redacted* forms, not on the generated input.
		// Two reasons. First, redaction is what the property is about. Second,
		// a generated `context` can contain a cycle, and after redaction it
		// cannot: the depth cap replaces everything at rootDepth >= 11 with
		// [TRUNCATED], so a cycle is unrolled into a finite tree at most 11
		// levels deep. The comparison therefore walks a finite structure and
		// cannot hang. (reflect.DeepEqual would in fact survive a cycle on its
		// own — it short-circuits on pointers it has already visited — but
		// relying on that would leave the JSON cross-check below with no such
		// protection, since json.Marshal fails outright on a cycle.)
		if !reflect.DeepEqual(once, twice) {
			pbtFailf(t, seed, i, generated,
				"redaction is not idempotent with token %q:\nApply once:  %s\nApply twice: %s",
				token, pbtInputJSON(once), pbtInputJSON(twice))
			continue
		}

		// The same claim restated on the wire representation, which is the form
		// the design's property table names ("reflect.DeepEqual on decoded
		// JSON") and the only form that matters to Better Stack. It also
		// catches a difference that DeepEqual on the in-memory tree could
		// tolerate — say, a redacted value whose Go type changed but whose JSON
		// encoding did not, or the reverse.
		//
		// Marshalling is attempted, not required: an event drawn under
		// hazardsAll may hold a value encoding/json rejects even after
		// redaction, and that is Property 3's subject, not this one. When
		// either side fails to marshal, the structural comparison above stands
		// alone for that draw.
		onceJSON, onceErr := json.Marshal(once)
		twiceJSON, twiceErr := json.Marshal(twice)
		if onceErr == nil && twiceErr == nil && string(onceJSON) != string(twiceJSON) {
			pbtFailf(t, seed, i, generated,
				"redacted JSON differs between one and two applications with token %q:\n"+
					"once:  %s\ntwice: %s",
				token, onceJSON, twiceJSON)
			continue
		}

		// `truncated` is a latch: conversion (Req 3.7, 3.9), the size cap
		// (Req 3.12), and the redaction depth cap (Req 10.10) all set it, and
		// nothing clears it. DeepEqual above already compares the field, but a
		// dedicated check names the failure, because "Apply cleared the
		// truncated latch on its second pass" is a specific bug that would
		// otherwise be reported as an opaque whole-event diff.
		if once.Truncated && !twice.Truncated {
			pbtFailf(t, seed, i, generated,
				"second Apply cleared truncated; the flag is a latch and must stay set")
			continue
		}

		if p4ContainsPlaceholder(once.Context, redactedPlaceholder, 1) ||
			p4RedactedFixedFields(once) {
			redactedSomething++
		}
		if once.Truncated || p4ContainsPlaceholder(once.Context, truncatedPlaceholder, 1) {
			truncatedSomething++
		}
	}

	// Without these, a Redactor whose Apply returned immediately would pass the
	// property above on every draw.
	if redactedSomething == 0 {
		t.Errorf("no generated event was redacted in %d draws (seed %d); the property passed vacuously",
			pbtIterations, seed)
	}
	if truncatedSomething == 0 {
		t.Errorf("no generated event hit the redaction depth cap in %d draws (seed %d); "+
			"idempotence of the [TRUNCATED] replacement went untested",
			pbtIterations, seed)
	}
}

// p4MaxSearchDepth bounds the coverage search below. Redacted output is at most
// 11 levels deep, since the depth cap replaces everything past rootDepth 10, so
// this bound never hides a placeholder that is legitimately there.
const p4MaxSearchDepth = 16

// p4ContainsPlaceholder reports whether want appears as a value within depth
// levels of v.
//
// redact_test.go's containsPlaceholder does the same walk unbounded, which is
// safe there because it is only ever handed an already-redacted, and therefore
// acyclic, tree. Here the bound is load-bearing: this helper measures how much
// redaction did, so it has to survive the very failure it is measuring. If a
// regression stopped redaction from cutting cycles, an unbounded walk would
// overflow the stack and take the whole test binary down, replacing a clean
// counterexample with a crash.
func p4ContainsPlaceholder(v any, want string, depth int) bool {
	if depth > p4MaxSearchDepth {
		return false
	}

	switch t := v.(type) {
	case string:
		return t == want
	case map[string]any:
		for _, child := range t {
			if p4ContainsPlaceholder(child, want, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if p4ContainsPlaceholder(child, want, depth+1) {
				return true
			}
		}
	}
	return false
}

// p4RedactedFixedFields reports whether redaction replaced any field of the
// fixed-shape error, request, or user objects, which containsPlaceholder cannot
// see because it only walks `context`.
func p4RedactedFixedFields(ev *Event) bool {
	if ev.Error != nil {
		if ev.Error.Message == redactedPlaceholder || ev.Error.Type == redactedPlaceholder {
			return true
		}
		for _, link := range ev.Error.Chain {
			if link == redactedPlaceholder {
				return true
			}
		}
	}
	if ev.Request != nil {
		if ev.Request.ID == redactedPlaceholder || ev.Request.Method == redactedPlaceholder ||
			ev.Request.Path == redactedPlaceholder {
			return true
		}
	}
	return ev.User != nil && ev.User.ID == redactedPlaceholder
}

// p4Token returns the Source_Token to redact this draw with, planting a
// non-empty token as a value inside ev so that the equality rule (Req 10.11)
// fires on a real event rather than never at all.
//
// The empty token is drawn often on purpose: it is the state whenever
// BETTERSTACK_SOURCE_TOKEN is unset, and it must not turn every empty string in
// the event into [REDACTED].
func p4Token(r *rand.Rand, ev *Event) string {
	if pbtChance(r, 35) {
		return ""
	}

	token := pbtPick(r, p4Tokens)
	p4PlantToken(r, ev, token)
	return token
}

// p4PlantTokenKey is the benign context key the token is planted under. It
// matches no sensitive substring, so a value planted here is replaced by the
// equality rule alone — if the key matched too, the equality rule would be
// masked and could break without this property noticing.
const p4PlantTokenKey = "upstream_note"

// p4PlantToken writes token into ev at shallow, well-known positions.
//
// Positions are shallow by design: planting into a randomly chosen nested spot
// would mean walking a possibly cyclic `context`, and the token would sometimes
// land under a sensitive key or past the depth cap, where the equality rule is
// masked by a rule that already fires. A nested-but-acyclic position is still
// covered — the list value below sits one level down inside `context`.
func p4PlantToken(r *rand.Rand, ev *Event, token string) {
	if ev.Context == nil {
		ev.Context = make(map[string]any, 2)
	}
	ev.Context[p4PlantTokenKey] = token
	if pbtChance(r, 50) {
		ev.Context["nested_list"] = []any{token, "other"}
	}

	if ev.Error != nil && pbtChance(r, 60) {
		ev.Error.Chain = append(ev.Error.Chain, token)
	}
	if ev.User != nil && pbtChance(r, 60) {
		ev.User.ID = token
	}
}

// ---------------------------------------------------------------------------
// Cycle-safe deep copy
// ---------------------------------------------------------------------------

// p4ref identifies one container by its address, so the copy can return the
// same copied container the second time it reaches it. That is what makes the
// copy terminate on a self-referential `context` and what makes the copy
// preserve sharing rather than silently unrolling it into a larger tree.
//
// The length and kind are part of the key because Go gives every zero-size
// allocation the same address: without them, an empty []any and an empty
// map[string]any could collide.
type p4ref struct {
	ptr  uintptr
	kind reflect.Kind
	len  int
}

// p4CopyEvent returns a deep copy of ev: every part of it that Redactor.Apply
// can mutate is a fresh object, so two copies can be redacted independently.
//
// The scalar top-level fields come along by struct assignment, including the
// unexported serializedLen — Apply does not touch it, and copying it keeps a
// difference there visible to DeepEqual rather than hidden.
func p4CopyEvent(ev *Event) *Event {
	if ev == nil {
		return nil
	}

	out := *ev
	if ev.Error != nil {
		errorInfo := *ev.Error
		if ev.Error.Chain != nil {
			// Apply rewrites chain elements in place, so the backing array must
			// not be shared. A nil chain stays nil: make would turn it into an
			// empty non-nil slice, which encoding/json treats differently under
			// omitempty and which DeepEqual reports as a difference.
			errorInfo.Chain = make([]string, len(ev.Error.Chain))
			copy(errorInfo.Chain, ev.Error.Chain)
		}
		out.Error = &errorInfo
	}
	if ev.Source != nil {
		source := *ev.Source
		out.Source = &source
	}
	if ev.Request != nil {
		request := *ev.Request
		out.Request = &request
	}
	if ev.User != nil {
		user := *ev.User
		out.User = &user
	}
	if ev.Context != nil {
		copied, _ := p4CopyValue(ev.Context, make(map[p4ref]any)).(map[string]any)
		out.Context = copied
	}
	return &out
}

// p4CopyValue deep-copies a normalized `context` value — the map[string]any,
// []any, and scalar shape conversion produces and the Redactor walks.
//
// Scalars are returned as they are: strings, numbers, bools, and nil are
// immutable in Go, so sharing them between two copies is not observable. Only
// the two container kinds are rebuilt, and each is registered in seen *before*
// its children are copied, which is what terminates a cycle: the recursive
// visit finds the half-built copy and points at it, producing a copy that is
// cyclic in the same places the original was.
func p4CopyValue(v any, seen map[p4ref]any) any {
	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			return v
		}
		key := p4ref{ptr: reflect.ValueOf(t).Pointer(), kind: reflect.Map, len: len(t)}
		if existing, ok := seen[key]; ok {
			return existing
		}
		out := make(map[string]any, len(t))
		seen[key] = out
		for name, child := range t {
			out[name] = p4CopyValue(child, seen)
		}
		return out

	case []any:
		if t == nil {
			return v
		}
		key := p4ref{ptr: reflect.ValueOf(t).Pointer(), kind: reflect.Slice, len: len(t)}
		if existing, ok := seen[key]; ok {
			return existing
		}
		out := make([]any, len(t))
		// Registered before the elements are filled in, and safe to register by
		// value: the copy below writes through the shared backing array, so the
		// slice stored here observes the same elements.
		seen[key] = out
		for i, child := range t {
			out[i] = p4CopyValue(child, seen)
		}
		return out

	default:
		return v
	}
}

// TestProperty4DeepCopyIsIndependentAndCycleSafe checks the one helper the
// property leans on. If p4CopyEvent returned two events that shared structure,
// the property above would compare a value with itself and pass no matter what
// the Redactor did, so the copy is verified directly rather than trusted.
func TestProperty4DeepCopyIsIndependentAndCycleSafe(t *testing.T) {
	original := &Event{
		Message: "boom",
		Context: map[string]any{
			"api_key": "k",
			"nested":  map[string]any{"list": []any{"a", "b"}},
		},
		Error:   &ErrorInfo{Message: "post failed", Type: "*url.Error", Chain: []string{"dial tcp"}},
		Source:  &SourceInfo{File: "shipper.go", Line: 12, Function: "logging.deliver"},
		Request: &RequestInfo{ID: "req-1", Method: "POST", Path: "/v1/logs"},
		User:    &UserInfo{ID: "u-1"},
	}

	copied := p4CopyEvent(original)
	if !reflect.DeepEqual(original, copied) {
		t.Fatalf("copy differs from the original:\ngot  %#v\nwant %#v", copied, original)
	}

	// Every mutable part must be a distinct object.
	if copied.Error == original.Error || copied.Source == original.Source ||
		copied.Request == original.Request || copied.User == original.User {
		t.Error("copy shares a fixed-shape sub-struct with the original")
	}
	if reflect.ValueOf(copied.Context).Pointer() == reflect.ValueOf(original.Context).Pointer() {
		t.Error("copy shares the context map with the original")
	}
	copiedNested := copied.Context["nested"].(map[string]any)
	originalNested := original.Context["nested"].(map[string]any)
	if reflect.ValueOf(copiedNested).Pointer() == reflect.ValueOf(originalNested).Pointer() {
		t.Error("copy shares a nested context map with the original")
	}

	// Mutating the copy the way Apply does must leave the original untouched.
	copiedNested["list"].([]any)[0] = redactedPlaceholder
	copied.Error.Chain[0] = redactedPlaceholder
	copied.User.ID = redactedPlaceholder
	if got := originalNested["list"].([]any)[0]; got != "a" {
		t.Errorf("mutating the copy changed the original nested list: %#v", got)
	}
	if original.Error.Chain[0] != "dial tcp" || original.User.ID != "u-1" {
		t.Errorf("mutating the copy changed the original: %#v", original)
	}

	// A cyclic context copies without hanging, and the copy is cyclic in the
	// same place rather than unrolled or flattened.
	cycle := pbtCyclicMap()
	cyclicCopy := p4CopyEvent(&Event{Context: map[string]any{"cycle": cycle}})
	copiedCycle := cyclicCopy.Context["cycle"].(map[string]any)
	if reflect.ValueOf(copiedCycle).Pointer() == reflect.ValueOf(cycle).Pointer() {
		t.Fatal("cyclic map was shared rather than copied")
	}
	self, ok := copiedCycle["self"].(map[string]any)
	if !ok || reflect.ValueOf(self).Pointer() != reflect.ValueOf(copiedCycle).Pointer() {
		t.Errorf("copied cycle does not point at itself: %#v", copiedCycle["self"])
	}

	// A cyclic slice too: pbtCyclicSlice's only element is the slice itself.
	cyclicSlice := pbtCyclicSlice()
	sliceCopy := p4CopyValue(map[string]any{"s": cyclicSlice}, make(map[p4ref]any)).(map[string]any)
	copiedSlice := sliceCopy["s"].([]any)
	if reflect.ValueOf(copiedSlice).Pointer() == reflect.ValueOf(cyclicSlice).Pointer() {
		t.Fatal("cyclic slice was shared rather than copied")
	}
	inner, ok := copiedSlice[0].([]any)
	if !ok || reflect.ValueOf(inner).Pointer() != reflect.ValueOf(copiedSlice).Pointer() {
		t.Errorf("copied cyclic slice does not point at itself: %#v", copiedSlice[0])
	}

	// And the redactor terminates on that copy, which is the combination the
	// property actually exercises.
	NewRedactor("").Apply(cyclicCopy)
	if !cyclicCopy.Truncated {
		t.Error("truncated = false after redacting a copied cyclic event, want true")
	}
}
