package logging

// Feature: betterstack-error-logging — Property 14: Redaction depth cap terminates.
//
// *For any* Log_Event — including events whose attribute graphs nest hundreds of
// levels deep or contain a cycle — redaction returns without panic and without
// unbounded recursion, replaces every value at `rootDepth >= 11` with
// [TRUNCATED], and sets `truncated: true` for exactly those events containing
// such a value.
//
// **Validates: Requirements 10.10, 12.9**
//
// Why this file uses no Shipper, no queue, and no HTTP server: the depth cap is
// unreachable on the conversion path, which bounds `context` at rootDepth 9 with
// one level of headroom. The cap is a backstop for hand-built events handed
// straight to the Redactor (Req 12.9) and for cyclic attribute graphs, so the
// only way to exercise it is to build the Event by hand and call Apply. That is
// also the P14 row of the design's property-test approach table: "hand-built
// deep and cyclic events straight to the Redactor, no Shipper. Oracle:
// depth-computing walker."
//
// What the property rests on, and therefore what a counterexample would mean:
// the depth cap is the *only* termination guarantee redactValue has. There is no
// visited set and no per-node bookkeeping, so a self-referential map terminates
// for exactly one reason — every descent increments rootDepth, and rootDepth 11
// returns a string without recursing. A failure here means either that the
// increment was lost on some edge (the walk would then not terminate at all, and
// the bounded-output assertion below is what turns that into a failure rather
// than a hang), or that `truncated` no longer reports what was actually cut.
//
// The oracle is deliberately independent of redactValue: p14PastCap recomputes
// depth from the Event root itself, so a sign error or an off-by-one inside the
// Redactor cannot be mirrored by the thing checking it. It does consult
// matchesSensitiveKey, because the key rule is a *different* axis — it fires
// before any descent, which is why a cycle behind a sensitive key is never
// walked and never truncates — and reimplementing the substring list here would
// test the copy rather than the rule.
//
// Example-based coverage of the same cap lives in redact_test.go
// (TestRedactorDepthCap, TestRedactorTerminatesOnCyclicEvent) and depth_test.go
// (TestRedactionDepthCapIsLiveForHandBuiltEvents). Those pin single points; this
// file is the universal statement over generated shapes.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
)

// p14LeafValue is the marker the depth-chain shapes put at the bottom, so a
// counterexample shows which value the walk was looking at.
const p14LeafValue = "p14-leaf"

// p14NestKey is the key each level of a generated chain nests under. It matches
// no sensitive substring, so a chain is bounded by depth alone and never cut
// short by the key rule.
const p14NestKey = "n"

// p14SourceToken is the Source_Token used on the draws that enable the equality
// rule (Req 10.11). Property 14 is about depth, but the two rules meet inside
// one walk, so a quarter of the draws run with a non-empty token to check that
// the depth cap still wins where they overlap: a token-valued string past the
// cap becomes [TRUNCATED], not [REDACTED], because the depth check runs first.
const p14SourceToken = "st_live_p14_0123456789"

// p14TokenKey is the benign context key the token is planted under.
const p14TokenKey = "note"

// p14BoundaryDepths straddle the cap exactly, expressed relative to the constant
// rather than as the literals 9, 10, 11, 12, so retuning maxRedactDepth keeps
// the generated shapes straddling it instead of silently sampling one side.
var p14BoundaryDepths = []rootDepth{
	maxRedactDepth - 1, // deepest retained, minus one
	maxRedactDepth,     // deepest retained value
	maxRedactDepth + 1, // first replaced value
	maxRedactDepth + 2, // well past the cap
}

// p14Expect is what a shape's *construction* implies about the cap, independent
// of both the Redactor and the oracle. It is a third opinion: the oracle is
// checked against it on every draw, so an oracle that answered "nothing is past
// the cap" for everything — which would make the property pass vacuously —
// fails instead.
type p14Expect int

const (
	// p14ExpectUnknown is for arbitrary generated events, whose depth is not
	// known by construction. Those draws rely on the oracle alone.
	p14ExpectUnknown p14Expect = iota
	// p14ExpectPastCap means the shape provably holds a value at rootDepth 11 or
	// deeper.
	p14ExpectPastCap
	// p14ExpectWithinCap means the shape provably holds no such value.
	p14ExpectWithinCap
)

// p14ShapeNames is the shape catalogue, and the coverage checklist asserted
// after the loop: a run that never generated a cycle, or never generated a
// wide-and-shallow event that must survive untouched, has not tested this
// property.
var p14ShapeNames = []string{
	"depth-straddling-the-cap",
	"hundreds-of-levels-deep",
	"self-referential-map",
	"self-referential-slice",
	"mutually-recursive-maps",
	"cycle-via-array-element",
	"cycle-behind-sensitive-key",
	"wide-and-shallow",
}

// p14Draw is one generated input together with everything a counterexample
// needs to be replayable.
type p14Draw struct {
	shape  string
	event  *Event
	token  string
	expect p14Expect
}

func TestProperty14RedactionDepthCapTerminates(t *testing.T) {
	r, seed := pbtRand(t)

	// Twice pbtIterations, so eight shapes plus the arbitrary-event draw each
	// get sampled tens of times rather than once or twice.
	const draws = pbtIterations * 2

	shapesSeen := make(map[string]int, len(p14ShapeNames))
	pastCapDraws, withinCapDraws, placeholdersSeen := 0, 0, 0

	for i := 0; i < draws; i++ {
		draw := p14GenDraw(r)
		shapesSeen[draw.shape]++

		// A cycle-safe deep copy of the input, taken before Apply runs. Apply
		// builds fresh maps rather than mutating the ones it walks, so today the
		// original graph survives redaction — but the oracle must read the input
		// as it was, not as the Redactor left it, or a future in-place
		// implementation would have the oracle grading the output against
		// itself.
		before := p4CopyEvent(draw.event)

		if !p14ApplyWithoutPanic(t, seed, i, NewRedactor(draw.token), draw) {
			continue
		}

		// The oracle: does any value in the input sit past the cap?
		pastCap := p14PastCap(before.Context, rootDepth(1))
		if pastCap {
			pastCapDraws++
		} else {
			withinCapDraws++
		}

		// Third opinion, from the shape's construction.
		switch {
		case draw.expect == p14ExpectPastCap && !pastCap:
			pbtFatalf(t, seed, i, before,
				"shape %q was built to nest a value past rootDepth %d, but the depth oracle "+
					"reports nothing past the cap; the oracle or the generator is wrong, so the "+
					"rest of this run would prove nothing",
				draw.shape, maxRedactDepth)
		case draw.expect == p14ExpectWithinCap && pastCap:
			pbtFatalf(t, seed, i, before,
				"shape %q was built to stay within rootDepth %d, but the depth oracle reports a "+
					"value past the cap; the oracle or the generator is wrong",
				draw.shape, maxRedactDepth)
		}

		// `truncated` for *exactly* those events holding such a value — with the
		// latch accounted for: conversion (Req 3.7, 3.9) and the size cap
		// (Req 3.12) also set it, the generator sets it on some draws, and Apply
		// must never clear it.
		wantTruncated := before.Truncated || pastCap
		if draw.event.Truncated != wantTruncated {
			pbtFailf(t, seed, i, before,
				"shape %q: truncated = %t after redaction, want %t (arrived as %t; the depth "+
					"oracle reports a value past rootDepth %d: %t)\nredacted event: %s",
				draw.shape, draw.event.Truncated, wantTruncated, before.Truncated,
				maxRedactDepth, pastCap, pbtInputJSON(draw.event))
			continue
		}

		// Every value past the cap replaced by [TRUNCATED], every value within
		// it left alone, and every key preserved — checked position by position
		// against the pristine input.
		if err := p14Verify(before.Context, draw.event.Context, rootDepth(1), "context", draw.token); err != nil {
			pbtFailf(t, seed, i, before, "shape %q: %v\nredacted event: %s",
				draw.shape, err, pbtInputJSON(draw.event))
			continue
		}

		// The "without unbounded recursion" clause, asserted as a bounded-depth
		// check on the *output*. Recursion depth is not directly observable, but
		// its consequence is: the cap stops descent at rootDepth 11, so a
		// redacted event cannot hold a value below that level. If some edge
		// stopped incrementing the depth, a cyclic input would recurse without
		// bound and the output — if one were ever produced — would be deeper
		// than the cap allows. p14MaxDepth stops walking well before the stack
		// does, so this reports a counterexample instead of hanging or crashing.
		if deepest := p14MaxDepth(draw.event.Context, rootDepth(1)); deepest > maxRedactDepth+1 {
			pbtFailf(t, seed, i, before,
				"shape %q: redacted output holds a value at rootDepth %d; the cap bounds descent "+
					"at %d, so nothing may sit below rootDepth %d\nredacted event: %s",
				draw.shape, deepest, maxRedactDepth, maxRedactDepth+1, pbtInputJSON(draw.event))
			continue
		}

		if p4ContainsPlaceholder(draw.event.Context, truncatedPlaceholder, 1) {
			placeholdersSeen++
		}
	}

	// Coverage. Every assertion above is satisfiable by an input that never
	// approaches the cap, so a run that generated only shallow events would pass
	// while testing nothing.
	for _, shape := range p14ShapeNames {
		if shapesSeen[shape] == 0 {
			t.Errorf("shape %q was never generated in %d draws (seed %d)", shape, draws, seed)
		}
	}
	if pastCapDraws == 0 {
		t.Errorf("no draw held a value past rootDepth %d in %d draws (seed %d); the depth cap "+
			"was never exercised", maxRedactDepth, draws, seed)
	}
	if withinCapDraws == 0 {
		t.Errorf("every draw held a value past rootDepth %d in %d draws (seed %d); a Redactor "+
			"that truncated unconditionally would have passed", maxRedactDepth, draws, seed)
	}
	if placeholdersSeen == 0 {
		t.Errorf("no redacted output contained a %q value in %d draws (seed %d)",
			truncatedPlaceholder, draws, seed)
	}
}

// TestProperty14CycleBehindSensitiveKeyIsNeverDescended is the one case worth
// stating on its own, because it is where the two rules disagree about what
// should happen and the key rule must win.
//
// A cycle behind a sensitive key is replaced with [REDACTED] by the key rule
// before any descent occurs, so the walk never reaches the cap and `truncated`
// stays false. Getting this backwards would be invisible in the aggregate — the
// event would still terminate and still carry a placeholder — but it would mean
// redaction was descending into a subtree it had already decided to discard, and
// reporting truncation for an event that lost nothing to depth.
func TestProperty14CycleBehindSensitiveKeyIsNeverDescended(t *testing.T) {
	// Spellings that exercise the case-insensitive substring rule (Req 10.1).
	for _, key := range []string{"session", "AUTHORIZATION", "refresh_token", "set-cookie"} {
		t.Run(key, func(t *testing.T) {
			cyclic := pbtCyclicMap()
			ev := &Event{
				DT:      formatTimestamp(pbtTimeBase),
				Level:   "ERROR",
				Context: map[string]any{key: cyclic, "ok": p14LeafValue},
			}

			NewRedactor("").Apply(ev)

			if got := ev.Context[key]; got != any(redactedPlaceholder) {
				t.Errorf("context.%s = %s, want %q: the key rule fires before any descent",
					key, p14Describe(got), redactedPlaceholder)
			}
			if ev.Truncated {
				t.Errorf("truncated = true, want false: the cycle behind %q was never descended "+
					"into, so nothing was lost to the depth cap (Req 10.10)", key)
			}
			if p4ContainsPlaceholder(ev.Context, truncatedPlaceholder, 1) {
				t.Errorf("context = %s, want no %q: descent must stop at the sensitive key",
					p14Describe(any(ev.Context)), truncatedPlaceholder)
			}
			// The caller's map is theirs: redaction may not edit it.
			if _, present := cyclic["self"]; !present || len(cyclic) != 2 {
				t.Errorf("redaction mutated the caller's map: %s", p14Describe(any(cyclic)))
			}
		})
	}
}

// TestProperty14DepthOracleMeasuresRootDepth checks the oracle and the depth
// builder against hand-computed positions, on both sides of the cap.
//
// The property leans on p14PastCap for its expected value, so an oracle that
// was itself off by one would move the property's boundary with the Redactor's
// and hide precisely the bug it exists to catch. The cross-checks against
// nestedContext, whose own offset is documented in redact_test.go, tie the two
// independent builders together.
func TestProperty14DepthOracleMeasuresRootDepth(t *testing.T) {
	// p14At(target) must place its leaf at exactly rootDepth target.
	for target := rootDepth(2); target <= 15; target++ {
		if got := p14MaxDepth(p14At(target, p14LeafValue), rootDepth(1)); got != target {
			t.Errorf("p14At(%d) placed its deepest value at rootDepth %d, want %d", target, got, target)
		}
	}

	// nestedContext(keys) places its leaf at rootDepth keys+1, per its own
	// documentation. Agreement between the two builders means neither drifted.
	for keys := 1; keys <= 12; keys++ {
		if got, want := p14MaxDepth(nestedContext(keys), rootDepth(1)), rootDepth(keys+1); got != want {
			t.Errorf("nestedContext(%d) deepest value at rootDepth %d, want %d", keys, got, want)
		}
	}

	cases := []struct {
		name string
		ctx  map[string]any
		want bool
	}{
		{"deepest retained value", p14At(maxRedactDepth, p14LeafValue), false},
		{"first replaced value", p14At(maxRedactDepth+1, p14LeafValue), true},
		{"far past the cap", p14At(maxRedactDepth+40, p14LeafValue), true},
		{"empty object at the cap", p14At(maxRedactDepth, map[string]any{}), false},
		{"nested empty object one past the cap", p14At(maxRedactDepth, map[string]any{"x": map[string]any{}}), true},
		{"self-referential map", map[string]any{"cycle": pbtCyclicMap()}, true},
		{"self-referential slice", map[string]any{"cycle": pbtCyclicSlice()}, true},
		{"mutually recursive maps", p14MutualMaps(), true},
		{"cycle via array element", p14CycleViaArray(), true},
		{"cycle behind a sensitive key", map[string]any{"session": pbtCyclicMap()}, false},
		{"shallow scalars", map[string]any{"a": "b", "n": 1.0, "list": []any{"x", nil}}, false},
		{"nil context", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p14PastCap(tc.ctx, rootDepth(1)); got != tc.want {
				t.Errorf("p14PastCap = %t, want %t", got, tc.want)
			}

			// The Redactor must agree with the oracle on the same input, which
			// is the property restated on one example.
			ev := &Event{Context: tc.ctx}
			NewRedactor("").Apply(ev)
			if ev.Truncated != tc.want {
				t.Errorf("truncated = %t after redaction, want %t (oracle: %t)",
					ev.Truncated, tc.want, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

// p14GenDraw returns one input: usually a hand-built shape whose depth is known
// by construction, and a quarter of the time an arbitrary generated Event with
// every hazard enabled, which is what makes the property's "for any Log_Event"
// quantifier more than a claim about eight shapes.
//
// hazardsAll is required for the arbitrary case rather than the serializable
// default, because that is the only hazard set under which genJSONValue emits a
// self-referential container.
func p14GenDraw(r *rand.Rand) p14Draw {
	draw := p14Draw{}

	if pbtChance(r, 25) {
		draw.shape = "arbitrary-generated"
		draw.event = genEventWith(r, hazardsAll)
		draw.expect = p14ExpectUnknown
	} else {
		shape, ctx, expect := p14GenShape(r)
		draw.shape = shape
		draw.expect = expect
		draw.event = &Event{
			DT:          formatTimestamp(genTime(r)),
			Level:       genLevel(r).String(),
			Message:     pbtPick(r, pbtWords),
			Service:     "fundis-backend",
			Environment: "production",
			// Generated already-latched on some draws, so "sets truncated for
			// exactly those events" is checked against a latch rather than
			// against a flag that only ever starts false.
			Truncated: pbtChance(r, 20),
			Context:   ctx,
		}
	}

	if pbtChance(r, 25) {
		draw.token = p14SourceToken
		if draw.event.Context == nil {
			draw.event.Context = make(map[string]any, 1)
		}
		// Planted shallow and under a benign key: the equality rule then fires
		// on its own, unmasked by the key rule and unmasked by the depth cap,
		// and the plant cannot change what the shape implies about depth.
		draw.event.Context[p14TokenKey] = draw.token
	}
	return draw
}

// p14GenShape returns one hand-built `context` graph, its name, and what its
// construction implies about the cap.
//
// The spread is chosen so the cap is approached from every direction that can
// break it: depths one either side of the boundary, a depth no stack could
// absorb without the cap, four distinct cycle topologies, a cycle the key rule
// reaches first, and a wide-but-shallow event that must come through untouched.
func p14GenShape(r *rand.Rand) (string, map[string]any, p14Expect) {
	shape := p14ShapeNames[r.Intn(len(p14ShapeNames))]

	switch shape {
	case "depth-straddling-the-cap":
		// Exactly at, one either side of, and just past the boundary.
		target := pbtPick(r, p14BoundaryDepths)
		expect := p14ExpectWithinCap
		if target > maxRedactDepth {
			expect = p14ExpectPastCap
		}
		return shape, p14At(target, p14LeafValue), expect

	case "hundreds-of-levels-deep":
		// Hundreds of levels: without the cap this is the case that would put
		// hundreds of frames on the stack per event, and thousands for a cycle.
		return shape, p14At(rootDepth(200+r.Intn(600)), p14LeafValue), p14ExpectPastCap

	case "self-referential-map":
		// Placed at a random depth, including at the cap itself, where the
		// cyclic map is replaced before it is ever entered.
		return shape, p14At(p14NestDepth(r), pbtCyclicMap()), p14ExpectPastCap

	case "self-referential-slice":
		return shape, p14At(p14NestDepth(r), pbtCyclicSlice()), p14ExpectPastCap

	case "mutually-recursive-maps":
		// Neither map contains itself; the cycle only exists through the pair,
		// which a naive "is this my own parent" check would miss.
		return shape, p14MutualMaps(), p14ExpectPastCap

	case "cycle-via-array-element":
		// The cycle is only reachable by descending through an array element, so
		// this fails if the slice branch of the walk forgets to increment depth.
		return shape, p14CycleViaArray(), p14ExpectPastCap

	case "cycle-behind-sensitive-key":
		return shape, map[string]any{
			genSensitiveKey(r): pbtCyclicMap(),
			"ok":               p14LeafValue,
		}, p14ExpectWithinCap

	default: // "wide-and-shallow"
		return shape, p14WideShallow(r), p14ExpectWithinCap
	}
}

// p14NestDepth returns a rootDepth from 2 to maxRedactDepth+1, so a nested
// subtree lands anywhere from the top of `context` to one level past the cap.
func p14NestDepth(r *rand.Rand) rootDepth {
	return rootDepth(2 + r.Intn(int(maxRedactDepth)))
}

// p14At returns a `context` object that places value at exactly rootDepth
// target.
//
// The offsets: `context` itself is at rootDepth 1, so the values of the keys in
// the returned map are at rootDepth 2, and each additional nesting level adds
// one. target below 2 is unreachable — there is no position inside `context`
// shallower than its own children — and is clamped rather than panicking, so a
// generator bug shows up as a failed depth assertion rather than as a crash in
// the generator.
//
// When value is itself a container, its children sit at target+1 and deeper.
// That is how the cycle shapes are positioned.
func p14At(target rootDepth, value any) map[string]any {
	if target < 2 {
		target = 2
	}
	nested := value
	for d := target; d > 2; d-- {
		nested = map[string]any{p14NestKey: nested}
	}
	return map[string]any{p14NestKey: nested}
}

// p14MutualMaps returns a context holding two maps that reference each other.
// Neither is self-referential; the cycle has period two.
func p14MutualMaps() map[string]any {
	first := map[string]any{"name": "first"}
	second := map[string]any{"name": "second", "first": first}
	first["second"] = second
	return map[string]any{"pair": first}
}

// p14CycleViaArray returns a context whose cycle passes through an array
// element: the array holds a map, and the map holds the array.
func p14CycleViaArray() map[string]any {
	owner := map[string]any{"name": "owner"}
	items := []any{p14LeafValue, owner}
	owner["items"] = items
	return map[string]any{"list": items}
}

// p14WideShallow returns a broad context whose deepest value sits at rootDepth
// 4, far inside the cap. It is the negative case: many values, many keys, and
// not one of them may be replaced or flagged.
func p14WideShallow(r *rand.Rand) map[string]any {
	ctx := make(map[string]any, 16)
	for keys := 8 + r.Intn(9); keys > 0; keys-- {
		// Suffixed so the keys are distinct, and drawn from the benign pool so
		// none of them triggers the key rule.
		key := pbtPick(r, pbtBenignKeys) + strconv.Itoa(keys)
		switch r.Intn(3) {
		case 0:
			ctx[key] = pbtPick(r, pbtWords)
		case 1:
			ctx[key] = []any{float64(r.Intn(100)), pbtPick(r, pbtWords), true, nil}
		default:
			ctx[key] = map[string]any{"inner": map[string]any{"leaf": pbtPick(r, pbtWords)}}
		}
	}
	return ctx
}

// ---------------------------------------------------------------------------
// The oracle and the verifier
// ---------------------------------------------------------------------------

// p14PastCap reports whether any value reachable from v sits at rootDepth
// greater than maxRedactDepth, where v itself is at rootDepth d.
//
// This is the oracle. It computes depth from the Event root on its own — every
// descent adds exactly one, arrays included — and calls nothing in redact.go
// that decides depth, so it cannot inherit the Redactor's off-by-one.
//
// It terminates on a cyclic graph for the same reason the Redactor does, and
// only that reason: the depth test comes first and answers without recursing.
//
// Two rules deliberately mirror redactValue's control flow, because they change
// *which values are reachable* rather than how deep they are:
//
//   - A sensitive key is not descended into. The key rule replaces the whole
//     value before the depth of its children is ever computed, so a cycle behind
//     such a key contributes nothing.
//   - A nil map or nil slice is returned unchanged and not walked, so it has no
//     children at all.
func p14PastCap(v any, d rootDepth) bool {
	if d > maxRedactDepth {
		// A value exists at this position, and it is past the cap. Answering
		// here rather than recursing is what makes this walk terminate on a
		// cyclic graph.
		return true
	}

	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			return false
		}
		for key, child := range t {
			if matchesSensitiveKey(key) {
				continue
			}
			if p14PastCap(child, d+1) {
				return true
			}
		}
	case []any:
		if t == nil {
			return false
		}
		for _, child := range t {
			if p14PastCap(child, d+1) {
				return true
			}
		}
	}
	return false
}

// p14Verify walks the pristine input and the redacted output in step and returns
// the first position where redaction did not do what the cap and the two
// selection rules require. in sits at rootDepth d, and path names it for the
// counterexample.
//
// It is depth-bounded by the same test the Redactor uses, so a cyclic input is
// walked to rootDepth 11 and no further.
func p14Verify(in, out any, d rootDepth, path, token string) error {
	if d > maxRedactDepth {
		// Req 10.10: replaced with the placeholder, whatever the original type
		// was, and nothing below it survives — a string has no children.
		if s, ok := out.(string); !ok || s != truncatedPlaceholder {
			return fmt.Errorf("%s sits at rootDepth %d, past the cap of %d, but redaction left %s "+
				"instead of %q", path, d, maxRedactDepth, p14Describe(out), truncatedPlaceholder)
		}
		return nil
	}

	switch t := in.(type) {
	case map[string]any:
		if t == nil {
			return nil // a nil map is returned unchanged
		}
		outMap, ok := out.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is an object at rootDepth %d, within the cap, but redaction "+
				"replaced it with %s", path, d, p14Describe(out))
		}
		if len(outMap) != len(t) {
			return fmt.Errorf("%s had %d keys and the redacted form has %d; every key is preserved "+
				"(Req 10.3)", path, len(t), len(outMap))
		}
		for key, child := range t {
			childPath := path + "." + key
			outChild, present := outMap[key]
			if !present {
				return fmt.Errorf("%s is absent from the redacted form", childPath)
			}
			if matchesSensitiveKey(key) {
				// The key rule fires before descent, so the whole value goes and
				// its depth is never measured. This is the assertion that a
				// cycle behind a sensitive key is not walked.
				if s, ok := outChild.(string); !ok || s != redactedPlaceholder {
					return fmt.Errorf("%s matches a sensitive key segment, so descent must not "+
						"happen at all, but redaction left %s instead of %q",
						childPath, p14Describe(outChild), redactedPlaceholder)
				}
				continue
			}
			if err := p14Verify(child, outChild, d+1, childPath, token); err != nil {
				return err
			}
		}
		return nil

	case []any:
		if t == nil {
			return nil
		}
		outSlice, ok := out.([]any)
		if !ok {
			return fmt.Errorf("%s is an array at rootDepth %d, within the cap, but redaction "+
				"replaced it with %s", path, d, p14Describe(out))
		}
		if len(outSlice) != len(t) {
			return fmt.Errorf("%s had %d elements and the redacted form has %d",
				path, len(t), len(outSlice))
		}
		for i, child := range t {
			elementPath := fmt.Sprintf("%s[%d]", path, i)
			if err := p14Verify(child, outSlice[i], d+1, elementPath, token); err != nil {
				return err
			}
		}
		return nil

	case string:
		// Within the cap a string is unchanged, unless it is the Source_Token
		// (Req 10.11).
		want := t
		if token != "" && t == token {
			want = redactedPlaceholder
		}
		if s, ok := out.(string); !ok || s != want {
			return fmt.Errorf("%s at rootDepth %d: redaction produced %s, want %s",
				path, d, p14Describe(out), p14Describe(any(want)))
		}
		return nil

	default:
		// Numbers, bools, nil, and any leaf the Redactor does not descend into
		// are returned untouched within the cap.
		if !reflect.DeepEqual(in, out) {
			return fmt.Errorf("%s at rootDepth %d is within the cap but redaction changed it from "+
				"%s to %s", path, d, p14Describe(in), p14Describe(out))
		}
		return nil
	}
}

// p14HardWalkLimit stops the output walk well past the cap but well short of the
// stack. It exists so that a redacted output which is deeper than the cap allows
// — or, in the worst case, still cyclic — is reported as a counterexample rather
// than crashing the test binary with a stack overflow, which no recover can
// catch and which would take the whole package's results with it.
const p14HardWalkLimit rootDepth = maxRedactDepth + 8

// p14MaxDepth returns the rootDepth of the deepest value reachable from v, where
// v itself is at rootDepth d, stopping at p14HardWalkLimit.
func p14MaxDepth(v any, d rootDepth) rootDepth {
	if d >= p14HardWalkLimit {
		return d
	}

	deepest := d
	switch t := v.(type) {
	case map[string]any:
		for _, child := range t {
			if got := p14MaxDepth(child, d+1); got > deepest {
				deepest = got
			}
		}
	case []any:
		for _, child := range t {
			if got := p14MaxDepth(child, d+1); got > deepest {
				deepest = got
			}
		}
	}
	return deepest
}

// p14ApplyWithoutPanic runs Apply and reports a panic as a counterexample rather
// than letting it abort the run, since "returns without panic" is part of the
// property.
//
// A stack overflow from lost termination is *not* recoverable — it is a fatal
// runtime error — which is why the property also asserts a bounded output depth:
// that is the observable consequence of bounded recursion, and it fails rather
// than hangs.
func p14ApplyWithoutPanic(t *testing.T, seed int64, iteration int, redactor *Redactor, draw p14Draw) (ok bool) {
	t.Helper()

	defer func() {
		if recovered := recover(); recovered != nil {
			pbtFailf(t, seed, iteration, draw.event,
				"shape %q: Redactor.Apply panicked: %v", draw.shape, recovered)
			ok = false
		}
	}()

	redactor.Apply(draw.event)
	return true
}

// p14Describe renders a value for a counterexample without formatting it with
// %v, which applies no cycle detection to maps and slices and would turn a
// failure report into a stack overflow.
func p14Describe(v any) string {
	switch t := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return pbtSafeString(t)
	case map[string]any:
		return fmt.Sprintf("<object with %d keys>", len(t))
	case []any:
		return fmt.Sprintf("<array of %d elements>", len(t))
	case bool, float64, float32, int, int64, uint64, json.Number:
		return fmt.Sprintf("%s(%v)", pbtTypeName(v), t)
	default:
		return "<" + pbtTypeName(v) + ">"
	}
}
