package logging

// Feature: betterstack-error-logging — Property 5: Redaction preserves structure.
//
// *For any* Log_Event, redaction leaves the set of key paths and the nesting
// shape unchanged, and changes only values whose key path has at least one
// segment matching the sensitive-substring set or whose value equals the
// Source_Token.
//
// **Validates: Requirements 10.1, 10.2, 10.3, 10.11**
//
// Why this is a separate claim from Property 4. Idempotence and structure
// preservation are independent: a Redactor that replaced every value in the
// event with [REDACTED] would be perfectly idempotent and would destroy the
// payload, and a Redactor that did nothing at all would preserve structure
// perfectly and ship credentials. P4 pins the first failure mode, this file
// pins the second, and the anti-vacuity counters at the bottom of the property
// are what stop the do-nothing Redactor from passing.
//
// The oracle is an *independent* key-path walker (p5Collect), deliberately not
// the Redactor's own traversal. A bug in redactValue's descent — a key skipped,
// an array flattened, a nil container mishandled — is exactly what this
// property is meant to catch, so reusing that descent to describe the expected
// output would make the property agree with the bug. p5Collect is run over the
// before tree and over the after tree with identical rules, and the two
// resulting path→shape maps are compared.
//
// The invariant has two documented exceptions, both of which the oracle
// predicts rather than flags:
//
//  1. A matched key replaces the *entire* value with the single string
//     [REDACTED], collapsing a matched object or array and everything beneath
//     it (Req 10.3). So the subtree under a matched key legitimately vanishes.
//     p5Collect therefore stops at a matched key on *both* trees and records
//     only "a value lives here", which makes the two maps line up while still
//     letting the value at that path be checked.
//  2. A value deeper than rootDepth 10 becomes [TRUNCATED] and is not descended
//     into (Req 10.10), which is a second legitimate shape change and the only
//     reason the walk terminates on a cyclic `context`. p5Collect stops at the
//     same depth on both trees for the same reason.
//
// What stays strict, and is the whole point of the property: keys are never
// deleted and never invented. At every level that survives those two
// exceptions, the key set must be identical, arrays must keep their length,
// containers must stay containers, and a value may only change if its path has
// a sensitive segment or the value itself equalled the Source_Token.
//
// This file holds only Property 5. Redaction's example-based cases live in
// redact_test.go and idempotence in prop_p4_redact_idempotent_test.go, so
// concurrently developed tests never contend for one file. The deep-copy and
// token-planting helpers are reused from the P4 file rather than duplicated.

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

func TestProperty5RedactionPreservesStructure(t *testing.T) {
	r, seed := pbtRand(t)

	// Coverage counters, asserted after the loop. "Structure preserved" is
	// satisfied vacuously by a Redactor that returns immediately, so a run in
	// which neither selection rule ever fired proves nothing.
	keyRuleDraws := 0
	tokenRuleDraws := 0
	depthCapDraws := 0

	for i := 0; i < pbtIterations; i++ {
		// Most draws use the serializable default. The rest enable every
		// hazard, which is the only way the harness generates a cyclic
		// `context`, and a cyclic context is what drives the depth-cap
		// exception this property has to predict rather than flag.
		var generated *Event
		if pbtChance(r, 30) {
			generated = genEventWith(r, hazardsAll)
		} else {
			generated = genEvent(r)
		}

		// Chosen after generation so the token can be planted as a value inside
		// the event: against random words the equality rule (Req 10.11) would
		// fire at probability zero. p4Token returns the empty token 35% of the
		// time, which is the mode where the equality rule is disabled entirely
		// and empty strings must survive.
		token := p4Token(r, generated)

		// Apply mutates in place — it replaces ev.Context wholesale and
		// rewrites ErrorInfo.Chain element by element — so the before state has
		// to be an independent deep copy taken first. p4CopyEvent is cycle-safe,
		// which matters for the hazardsAll draws.
		before := p4CopyEvent(generated)
		after := p4CopyEvent(generated)
		NewRedactor(token).Apply(after)

		beforePaths := make(map[string]p5Entry)
		afterPaths := make(map[string]p5Entry)
		// The `context` object is at rootDepth 1, so its direct children are at
		// rootDepth 2 — the normalization the Redactor uses when it starts its
		// own walk at rootDepth(1).
		if before.Context != nil {
			p5Collect(before.Context, "context", rootDepth(1), beforePaths)
		}
		if after.Context != nil {
			p5Collect(after.Context, "context", rootDepth(1), afterPaths)
		}

		result := p5Diff(beforePaths, afterPaths, token)
		fixed := p5DiffFixedFields(before, after, token)
		violations := append(result.violations, fixed.violations...)

		// `truncated` is exactly predictable: Apply sets it when, and only when,
		// its context walk reached a value past the depth cap, and it never
		// clears the latch conversion or the size cap may already have set.
		wantTruncated := before.Truncated || result.depthPruned > 0
		if after.Truncated != wantTruncated {
			violations = append(violations, fmt.Sprintf(
				"truncated = %t, want %t (before = %t, values past the depth cap = %d)",
				after.Truncated, wantTruncated, before.Truncated, result.depthPruned))
		}

		if len(violations) > 0 {
			pbtFailf(t, seed, i, generated,
				"redaction did not preserve structure with token %q:\n%s\nbefore: %s\nafter:  %s",
				token, p5FormatViolations(violations),
				pbtInputJSON(before), pbtInputJSON(after))
			continue
		}

		if result.keyRuleFired > 0 {
			keyRuleDraws++
		}
		if result.tokenRuleFired+fixed.tokenRuleFired > 0 {
			tokenRuleDraws++
		}
		if result.depthPruned > 0 {
			depthCapDraws++
		}
	}

	// Without these, a Redactor whose Apply returned immediately would satisfy
	// every assertion above on every draw.
	if keyRuleDraws == 0 {
		t.Errorf("the sensitive-key rule never replaced a value in %d draws (seed %d); "+
			"structure preservation passed vacuously, since a Redactor that does nothing "+
			"preserves structure perfectly", pbtIterations, seed)
	}
	if tokenRuleDraws == 0 {
		t.Errorf("the Source_Token equality rule never replaced a value in %d draws (seed %d); "+
			"Req 10.11's second selection rule went untested", pbtIterations, seed)
	}
	if depthCapDraws == 0 {
		t.Errorf("no generated event held a value past rootDepth %d in %d draws (seed %d); "+
			"the depth-cap shape exception (Req 10.10) went untested",
			maxRedactDepth, pbtIterations, seed)
	}
	t.Logf("coverage over %d draws: key rule fired in %d, token rule in %d, depth cap in %d",
		pbtIterations, keyRuleDraws, tokenRuleDraws, depthCapDraws)
}

// ---------------------------------------------------------------------------
// Independent key-path walker
// ---------------------------------------------------------------------------

// p5Kind is the structural shape of a value, classified by the same type switch
// the Redactor's walk uses — map[string]any, []any, string, everything else —
// because those are the distinctions that decide whether a value is descended
// into. Classifying a json.Number as a string here, for instance, would assert
// a shape the Redactor never claims.
type p5Kind uint8

const (
	p5KindObject p5Kind = iota
	p5KindArray
	p5KindString
	p5KindOther
)

// p5Prune records why the walker stopped at a path without describing the value
// there. The two reasons are the two documented shape exceptions, and the shape
// is deliberately *not* recorded at a pruned node: at a matched key the before
// value may be an object and the after value is the string [REDACTED], so
// comparing shapes there would report the specified behaviour as a violation.
type p5Prune uint8

const (
	p5PruneNone p5Prune = iota
	// p5PruneSensitiveKey marks the value of a key that matches a sensitive
	// substring. The subtree beneath it is not walked, on either tree, because
	// Req 10.3 collapses all of it into one string.
	p5PruneSensitiveKey
	// p5PruneDepth marks a value past the redaction depth cap (Req 10.10).
	p5PruneDepth
)

// p5Entry is one node of the walk: what shape it had and, for a leaf or a
// pruned node, the raw value found there.
type p5Entry struct {
	kind   p5Kind
	prune  p5Prune
	length int
	goType string
	value  any
}

// shape renders the part of an entry that must be identical before and after.
func (e p5Entry) shape() string {
	switch e.prune {
	case p5PruneSensitiveKey:
		return "<value of a sensitive key>"
	case p5PruneDepth:
		return "<value past the depth cap>"
	}
	switch e.kind {
	case p5KindObject:
		return fmt.Sprintf("object(%d keys)", e.length)
	case p5KindArray:
		return fmt.Sprintf("array(%d)", e.length)
	case p5KindString:
		return "string"
	default:
		return "scalar " + e.goType
	}
}

// p5Collect walks a normalized `context` value and records one entry per key
// path into out, where d is the rootDepth of v.
//
// This is the oracle, and it is written to be readable in one sitting rather
// than to mirror redact.go: nothing here consults the Redactor, so a divergence
// between the two walks shows up as a failure instead of cancelling out.
//
// Termination on a self-referential value rests on the depth cap alone, exactly
// as the Redactor's does — no visited set, at most 11 levels of recursion — and
// that is not an accident of convenience: the oracle has to survive being
// handed the same cyclic input the production walk survives.
//
// Paths are built with strconv.Quote on each key, which makes the encoding
// injective: a key containing `[`, `.`, or invalid UTF-8 cannot collide with a
// different key path, and array indices are unambiguous against object keys.
func p5Collect(v any, path string, d rootDepth, out map[string]p5Entry) {
	if d > maxRedactDepth {
		out[path] = p5Entry{prune: p5PruneDepth, value: v}
		return
	}

	switch t := v.(type) {
	case map[string]any:
		// A nil map needs no special case: len is 0 and ranging over it yields
		// nothing, which is precisely how the Redactor treats it too.
		out[path] = p5Entry{kind: p5KindObject, length: len(t)}
		for key, child := range t {
			childPath := path + "[" + strconv.Quote(key) + "]"
			// One map key is one key path segment (Req 10.1), and the match is
			// tested before descending — the same order the Redactor uses, so a
			// value that is both under a matched key and past the depth cap is
			// attributed to the key rule on both sides.
			if matchesSensitiveKey(key) {
				out[childPath] = p5Entry{prune: p5PruneSensitiveKey, value: child}
				continue
			}
			p5Collect(child, childPath, d+1, out)
		}

	case []any:
		out[path] = p5Entry{kind: p5KindArray, length: len(t)}
		for index, child := range t {
			// Array elements carry no key of their own, so no key match is
			// tested here: they are subject only to the depth cap and the
			// Source_Token equality rule.
			p5Collect(child, fmt.Sprintf("%s[%d]", path, index), d+1, out)
		}

	case string:
		out[path] = p5Entry{kind: p5KindString, value: t}

	default:
		// Numbers, bools, nil, json.Number, and any other leaf. The Go type is
		// part of the shape, so a number silently becoming a bool is reported.
		out[path] = p5Entry{kind: p5KindOther, goType: fmt.Sprintf("%T", v), value: v}
	}
}

// ---------------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------------

// p5Result is the outcome of one comparison: what was wrong, and how much each
// selection rule actually did, which is what the anti-vacuity guards consume.
type p5Result struct {
	violations []string
	// keyRuleFired counts values replaced because their key matched, excluding
	// values that were already the placeholder and so prove nothing.
	keyRuleFired int
	// tokenRuleFired counts values replaced because they equalled the token.
	tokenRuleFired int
	// depthPruned counts values past the depth cap, whether or not the Redactor
	// replaced them correctly, so the `truncated` prediction is independent of
	// whether the replacement itself was right.
	depthPruned int
}

// p5Diff compares two key-path maps produced by p5Collect.
//
// It returns violations rather than calling t.Errorf so that the comparison
// itself can be unit-tested against deliberately damaged input — a checker that
// silently accepts everything is the one failure mode a property test cannot
// detect on its own.
func p5Diff(before, after map[string]p5Entry, token string) p5Result {
	var result p5Result

	for path, want := range before {
		got, present := after[path]
		if !present {
			result.violations = append(result.violations, fmt.Sprintf(
				"key path %s disappeared; keys are never deleted (Req 10.3)", path))
			continue
		}

		switch want.prune {
		case p5PruneSensitiveKey:
			// The whole value goes, whatever its type, and the key stays.
			if got.value != redactedPlaceholder {
				result.violations = append(result.violations, fmt.Sprintf(
					"key path %s matches a sensitive substring but its value is %s, want the string %q (Req 10.1, 10.3)",
					path, p5DescribeValue(got.value), redactedPlaceholder))
				continue
			}
			if want.value != redactedPlaceholder {
				result.keyRuleFired++
			}

		case p5PruneDepth:
			result.depthPruned++
			if got.value != truncatedPlaceholder {
				result.violations = append(result.violations, fmt.Sprintf(
					"key path %s is past rootDepth %d but its value is %s, want the string %q (Req 10.10)",
					path, maxRedactDepth, p5DescribeValue(got.value), truncatedPlaceholder))
			}

		default:
			if got.prune != p5PruneNone || got.kind != want.kind ||
				got.length != want.length || got.goType != want.goType {
				result.violations = append(result.violations, fmt.Sprintf(
					"shape changed at key path %s: %s became %s; redaction changes values, not shapes",
					path, want.shape(), got.shape()))
				continue
			}
			if violation, fired := p5CheckValue(path, want, got, token); violation != "" {
				result.violations = append(result.violations, violation)
			} else if fired {
				result.tokenRuleFired++
			}
		}
	}

	for path := range after {
		if _, present := before[path]; !present {
			result.violations = append(result.violations, fmt.Sprintf(
				"key path %s appeared; redaction invents no keys (Req 10.3)", path))
		}
	}

	// Map iteration order is random, so an unsorted report would differ between
	// runs of the same seed and make a counterexample harder to compare.
	sort.Strings(result.violations)
	return result
}

// p5CheckValue applies the value half of the property to a surviving leaf: it
// may only change if it equalled the Source_Token, and then only to
// [REDACTED]. It reports the violation, if any, and whether the equality rule
// fired.
//
// Containers reach here too, and for them there is nothing to compare beyond
// the shape already checked by the caller: their children are separate entries.
func p5CheckValue(path string, want, got p5Entry, token string) (violation string, tokenRuleFired bool) {
	switch want.kind {
	case p5KindString:
		wantValue, _ := want.value.(string)
		gotValue, _ := got.value.(string)
		switch {
		case token != "" && wantValue == token:
			// The one permitted change to a value under a benign key — and a
			// required one, not merely permitted: Req 10.11 says a value equal
			// to the Source_Token is replaced. Checking it in both directions
			// is what stops a redactor that skipped this rule from passing a
			// property whose headline claim is "structure is preserved".
			if gotValue != redactedPlaceholder {
				return fmt.Sprintf(
					"key path %s equalled the Source_Token and is still %s, want %q (Req 10.11)",
					path, p5DescribeValue(got.value), redactedPlaceholder), false
			}
			return "", true
		case gotValue == wantValue:
			return "", false
		case wantValue == "" && gotValue == redactedPlaceholder:
			// Called out separately because it is the specific regression an
			// empty Source_Token invites: with the empty-token guard removed,
			// every empty string in every event becomes [REDACTED].
			return fmt.Sprintf(
				"key path %s held the empty string and became %q; an empty Source_Token disables "+
					"the equality rule entirely (Req 10.11)", path, redactedPlaceholder), false
		default:
			return fmt.Sprintf(
				"value changed at key path %s: %s became %s, but the path has no sensitive segment "+
					"and the value did not equal the Source_Token (Req 10.11)",
				path, p5DescribeValue(want.value), p5DescribeValue(got.value)), false
		}

	case p5KindOther:
		if !p5SameScalar(want.value, got.value) {
			return fmt.Sprintf(
				"value changed at key path %s: %s became %s; no content inspection is performed on "+
					"non-string leaves (Req 10.11)",
				path, p5DescribeValue(want.value), p5DescribeValue(got.value)), false
		}
		return "", false

	default:
		return "", false
	}
}

// p5SameScalar compares two non-container leaves. NaN is handled explicitly
// because it is not equal to itself, so a NaN that survived redaction untouched
// would otherwise be reported as a changed value.
func p5SameScalar(a, b any) bool {
	if aFloat, ok := a.(float64); ok {
		if bFloat, ok := b.(float64); ok && math.IsNaN(aFloat) && math.IsNaN(bFloat) {
			return true
		}
	}
	return reflect.DeepEqual(a, b)
}

// p5DescribeValue renders a value for a failure message: strings quoted and
// elided through the harness's helper, containers named rather than dumped,
// since the interesting part of a shape failure is the kind and not the payload.
func p5DescribeValue(v any) string {
	switch t := v.(type) {
	case string:
		return pbtSafeString(t)
	case map[string]any:
		return fmt.Sprintf("object(%d keys)", len(t))
	case []any:
		return fmt.Sprintf("array(%d)", len(t))
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v (%T)", t, t)
	}
}

// p5MaxReportedViolations bounds a failure report. One structural break — a
// dropped key high in the tree — cascades into a violation per descendant path,
// and 500 of them would bury the first and most informative line.
const p5MaxReportedViolations = 8

func p5FormatViolations(violations []string) string {
	shown := violations
	suffix := ""
	if len(shown) > p5MaxReportedViolations {
		shown = shown[:p5MaxReportedViolations]
		suffix = fmt.Sprintf("\n  ... and %d more", len(violations)-p5MaxReportedViolations)
	}
	report := ""
	for _, violation := range shown {
		report += "  - " + violation + "\n"
	}
	return report + suffix
}

// ---------------------------------------------------------------------------
// Fixed-shape subtrees and unscoped fields
// ---------------------------------------------------------------------------

// p5FixedResult mirrors p5Result for the parts of the Event that are typed
// structs rather than maps.
type p5FixedResult struct {
	violations     []string
	tokenRuleFired int
}

// p5DiffFixedFields checks `error`, `request`, and `user`, whose keys are fixed
// field names rather than generated ones, plus the top-level fields the
// Redactor is out of scope for (Req 10.2).
//
// Their "structure" claim is narrower but not empty: the objects must stay
// present or absent as they were, and error.chain must keep its length, since
// an array element dropped there would lose a link of the error chain. None of
// the field names message/type/chain/id/method/path contains a sensitive
// substring, so in practice only the Source_Token equality rule fires here —
// the key rule is still evaluated so that renaming a field to something like
// session_id would start redacting it.
func p5DiffFixedFields(before, after *Event, token string) p5FixedResult {
	var result p5FixedResult

	check := func(path, key, wantBefore, gotAfter string) {
		expected := wantBefore
		if matchesSensitiveKey(key) || (token != "" && wantBefore == token) {
			expected = redactedPlaceholder
		}
		if gotAfter != expected {
			result.violations = append(result.violations, fmt.Sprintf(
				"%s = %s, want %s", path, pbtSafeString(gotAfter), pbtSafeString(expected)))
			return
		}
		if expected != wantBefore {
			result.tokenRuleFired++
		}
	}

	if (before.Error == nil) != (after.Error == nil) {
		result.violations = append(result.violations,
			"the presence of `error` changed; redaction adds and removes no objects")
	} else if before.Error != nil {
		check("error.message", "message", before.Error.Message, after.Error.Message)
		check("error.type", "type", before.Error.Type, after.Error.Type)
		if len(before.Error.Chain) != len(after.Error.Chain) {
			result.violations = append(result.violations, fmt.Sprintf(
				"error.chain has %d links, want %d; array lengths are preserved",
				len(after.Error.Chain), len(before.Error.Chain)))
		} else {
			for i, link := range before.Error.Chain {
				check(fmt.Sprintf("error.chain[%d]", i), "chain", link, after.Error.Chain[i])
			}
		}
	}

	if (before.Request == nil) != (after.Request == nil) {
		result.violations = append(result.violations,
			"the presence of `request` changed; redaction adds and removes no objects")
	} else if before.Request != nil {
		check("request.id", "id", before.Request.ID, after.Request.ID)
		check("request.method", "method", before.Request.Method, after.Request.Method)
		check("request.path", "path", before.Request.Path, after.Request.Path)
	}

	if (before.User == nil) != (after.User == nil) {
		result.violations = append(result.violations,
			"the presence of `user` changed; redaction adds and removes no objects")
	} else if before.User != nil {
		check("user.id", "id", before.User.ID, after.User.ID)
	}

	// Out of scope entirely (Req 10.2): shipper-generated fields, not
	// caller-supplied attribute values. `truncated` is the one field the
	// Redactor may write, and the caller checks it against its own prediction.
	if after.DT != before.DT || after.Level != before.Level || after.Message != before.Message ||
		after.Service != before.Service || after.Environment != before.Environment {
		result.violations = append(result.violations, fmt.Sprintf(
			"an unscoped top-level field changed: dt/level/message/service/environment = "+
				"%q/%q/%s/%s/%s, want %q/%q/%s/%s/%s (Req 10.2)",
			after.DT, after.Level, pbtSafeString(after.Message), pbtSafeString(after.Service),
			pbtSafeString(after.Environment),
			before.DT, before.Level, pbtSafeString(before.Message), pbtSafeString(before.Service),
			pbtSafeString(before.Environment)))
	}
	if !reflect.DeepEqual(before.Source, after.Source) {
		result.violations = append(result.violations, fmt.Sprintf(
			"source = %#v, want %#v; `source` is outside the Redactor's scope (Req 10.2)",
			after.Source, before.Source))
	}

	return result
}

// ---------------------------------------------------------------------------
// Oracle self-tests
// ---------------------------------------------------------------------------

// TestProperty5OracleDetectsStructureDamage verifies that the comparison
// actually rejects the things it claims to reject. The property above can only
// be trusted to the extent p5Diff is not a function that returns no
// violations, and no amount of random input would reveal that.
func TestProperty5OracleDetectsStructureDamage(t *testing.T) {
	const token = "st_live_abcdef123456"

	// A structurally rich context: a matched key over an object, a benign
	// nested object, an array, scalars, and the token under a benign key.
	source := func() map[string]any {
		return map[string]any{
			"api_key":       map[string]any{"inner": "v"},
			"upstream_note": token,
			"count":         float64(3),
			"nested": map[string]any{
				"list":       []any{"a", token, float64(1)},
				"session_id": "s-1",
				"empty":      "",
			},
			"deep": nestedContext(int(maxRedactDepth)),
		}
	}

	collect := func(context map[string]any) map[string]p5Entry {
		out := make(map[string]p5Entry)
		p5Collect(context, "context", rootDepth(1), out)
		return out
	}

	// The undamaged case: the real Redactor over this context must produce no
	// violations, and both selection rules and the depth cap must be exercised
	// by it, so the damaged cases below are perturbations of a passing run.
	before := collect(source())
	redacted := &Event{Context: source()}
	NewRedactor(token).Apply(redacted)
	baseline := p5Diff(before, collect(redacted.Context), token)
	if len(baseline.violations) != 0 {
		t.Fatalf("the real Redactor was reported as violating the property:\n%s",
			p5FormatViolations(baseline.violations))
	}
	if baseline.keyRuleFired == 0 || baseline.tokenRuleFired == 0 || baseline.depthPruned == 0 {
		t.Fatalf("baseline coverage is incomplete: keyRule=%d tokenRule=%d depthPruned=%d",
			baseline.keyRuleFired, baseline.tokenRuleFired, baseline.depthPruned)
	}
	if !redacted.Truncated {
		t.Error("truncated = false after redacting a context nested past the depth cap, want true")
	}

	// Each case damages the redacted output the way a specific regression in
	// the traversal would, and the oracle must reject every one of them.
	damage := map[string]func(after map[string]any){
		"a key was deleted": func(after map[string]any) {
			delete(after, "count")
		},
		"a key was invented": func(after map[string]any) {
			after["extra"] = "x"
		},
		"a nested key was deleted": func(after map[string]any) {
			delete(after["nested"].(map[string]any), "empty")
		},
		"an array element was dropped": func(after map[string]any) {
			list := after["nested"].(map[string]any)["list"].([]any)
			after["nested"].(map[string]any)["list"] = list[:2]
		},
		"an object was flattened into a string": func(after map[string]any) {
			after["nested"] = "flattened"
		},
		"a benign value was redacted": func(after map[string]any) {
			after["count"] = redactedPlaceholder
		},
		"an empty string was redacted": func(after map[string]any) {
			after["nested"].(map[string]any)["empty"] = redactedPlaceholder
		},
		"a matched key was left unredacted": func(after map[string]any) {
			after["api_key"] = map[string]any{"inner": "v"}
		},
		"a matched nested key was left unredacted": func(after map[string]any) {
			after["nested"].(map[string]any)["session_id"] = "s-1"
		},
		"the token was left in place": func(after map[string]any) {
			after["upstream_note"] = token
		},
		"a truncated value was left in place": func(after map[string]any) {
			after["deep"] = nestedContext(int(maxRedactDepth))
		},
		"a scalar changed type": func(after map[string]any) {
			after["count"] = true
		},
	}

	for name, apply := range damage {
		t.Run(name, func(t *testing.T) {
			damaged := &Event{Context: source()}
			NewRedactor(token).Apply(damaged)
			apply(damaged.Context)

			if result := p5Diff(before, collect(damaged.Context), token); len(result.violations) == 0 {
				t.Errorf("the oracle accepted damaged output (%s); it must report a violation", name)
			}
		})
	}
}

// TestProperty5OracleAcceptsUnchangedValuesWithEmptyToken pins the empty-token
// mode of the oracle itself: with no token, nothing may be replaced except by
// the key rule, and an empty string must be reported as a violation if it
// becomes a placeholder.
func TestProperty5OracleAcceptsUnchangedValuesWithEmptyToken(t *testing.T) {
	context := map[string]any{"note": "", "list": []any{"", "keep"}, "token": ""}

	before := make(map[string]p5Entry)
	p5Collect(context, "context", rootDepth(1), before)

	ev := &Event{Context: map[string]any{"note": "", "list": []any{"", "keep"}, "token": ""}}
	NewRedactor("").Apply(ev)

	after := make(map[string]p5Entry)
	p5Collect(ev.Context, "context", rootDepth(1), after)

	result := p5Diff(before, after, "")
	if len(result.violations) != 0 {
		t.Errorf("empty token produced violations:\n%s", p5FormatViolations(result.violations))
	}
	// The one key here that matches a sensitive substring is "token" itself, so
	// the key rule must still have fired even with no Source_Token configured.
	if result.keyRuleFired != 1 {
		t.Errorf("keyRuleFired = %d, want 1", result.keyRuleFired)
	}
	if result.tokenRuleFired != 0 {
		t.Errorf("tokenRuleFired = %d, want 0; an empty token disables the equality rule",
			result.tokenRuleFired)
	}
}

// TestProperty5FixedFieldOracleDetectsDamage does for p5DiffFixedFields what
// the case table above does for p5Diff.
func TestProperty5FixedFieldOracleDetectsDamage(t *testing.T) {
	const token = "st_live_abcdef123456"

	source := func() *Event {
		return &Event{
			DT:          "2024-01-01T00:00:00.000Z",
			Level:       "ERROR",
			Message:     "boom " + token,
			Service:     "fundis-backend",
			Environment: "production",
			Source:      &SourceInfo{File: "token.go", Line: 12, Function: "auth.SecretPath"},
			Error:       &ErrorInfo{Message: token, Type: "*url.Error", Chain: []string{token, "dial tcp"}},
			Request:     &RequestInfo{ID: "req-1", Method: "POST", Path: "/v1/logs"},
			User:        &UserInfo{ID: token},
		}
	}

	before := source()
	after := source()
	NewRedactor(token).Apply(after)

	baseline := p5DiffFixedFields(before, after, token)
	if len(baseline.violations) != 0 {
		t.Fatalf("the real Redactor was reported as violating the property on fixed fields:\n%s",
			p5FormatViolations(baseline.violations))
	}
	// error.message, error.chain[0], and user.id all equal the token.
	if baseline.tokenRuleFired != 3 {
		t.Errorf("tokenRuleFired = %d, want 3", baseline.tokenRuleFired)
	}

	damage := map[string]func(ev *Event){
		"the token survived in error.chain": func(ev *Event) { ev.Error.Chain[0] = token },
		"a chain link was dropped":          func(ev *Event) { ev.Error.Chain = ev.Error.Chain[:1] },
		"error was removed":                 func(ev *Event) { ev.Error = nil },
		"user was removed":                  func(ev *Event) { ev.User = nil },
		"a benign field was redacted":       func(ev *Event) { ev.Request.Method = redactedPlaceholder },
		"an unscoped field changed":         func(ev *Event) { ev.Message = redactedPlaceholder },
		"source was rewritten":              func(ev *Event) { ev.Source.Function = redactedPlaceholder },
	}

	for name, apply := range damage {
		t.Run(name, func(t *testing.T) {
			damaged := source()
			NewRedactor(token).Apply(damaged)
			apply(damaged)

			if result := p5DiffFixedFields(before, damaged, token); len(result.violations) == 0 {
				t.Errorf("the oracle accepted damaged output (%s); it must report a violation", name)
			}
		})
	}
}
