package logging

// The two nesting caps are measured from different origins: maxContextDepth is
// counted from the `context` object (ctxLevel), maxRedactDepth from the
// Log_Event root (rootDepth), with
//
//	rootDepth(v) = ctxLevel(v) + 1        for any v inside context
//
// The invariant that matters is a *relationship* between them, not either
// value: once both are normalized to rootDepth, the conversion cap must be
// strictly tighter than the redaction cap, so redaction can never replace a
// value that conversion deliberately chose to retain. The tests here assert
// that relationship — on the constants, and then end to end on real values —
// so retuning either constant in isolation fails the build rather than
// silently changing which payloads survive.

import (
	"fmt"
	"log/slog"
	"testing"
)

// TestDepthCapsNormalizeConsistently is the guard on the relationship itself.
//
// It deliberately asserts no literal: 8 and 10 are tuning decisions, and a
// future change to either is legitimate. What is not legitimate is a change
// that makes the redaction cap reachable by the conversion path, because
// redaction would then truncate values conversion retained and the `truncated`
// flag would start latching on payloads that were never too deep to ship.
func TestDepthCapsNormalizeConsistently(t *testing.T) {
	// The normalization above only holds while a direct child of `context`
	// starts at ctxLevel 1 and `context` itself sits at rootDepth 1. That is
	// the origin definition rather than a tuning knob, and every +1 below
	// depends on it, so it is checked first: if the origin moves, the two
	// comparisons that follow stop meaning what they say.
	if rootContextLevel != 1 {
		t.Fatalf("rootContextLevel = %d, want 1\n\n"+
			"rootContextLevel is the ctxLevel of a direct child of `context`, and the whole\n"+
			"normalization rootDepth = ctxLevel + 1 is derived from it. Changing it changes\n"+
			"the meaning of maxContextDepth and of every depth assertion in this package.",
			rootContextLevel)
	}

	// Deepest value conversion retains, normalized: a value at
	// ctxLevel maxContextDepth sits at rootDepth maxContextDepth+1 (Req 3.9).
	// Shallowest value redaction replaces: rootDepth maxRedactDepth+1
	// (Req 10.10).
	if rootDepth(maxContextDepth)+1 >= maxRedactDepth+1 {
		t.Fatalf("depth caps have drifted: conversion retains values down to rootDepth %d, "+
			"but redaction replaces values at rootDepth %d and deeper\n\n"+
			"maxContextDepth = %d (ctxLevel, counted from the `context` object)\n"+
			"maxRedactDepth  = %d (rootDepth, counted from the Log_Event root)\n"+
			"normalization    rootDepth = ctxLevel + 1, because `context` is a direct child of the root\n\n"+
			"What the relationship means: conversion runs first and bounds everything under\n"+
			"`context`; redaction then walks the event it produced. While the conversion cap is\n"+
			"strictly tighter in rootDepth, the Redactor's cap is unreachable on that path, so\n"+
			"no value conversion chose to retain is ever replaced with %q and `truncated` never\n"+
			"latches for a depth the conversion cap already accepted.\n\n"+
			"What to do: this fires when one constant was retuned without the other. Either lower\n"+
			"maxContextDepth so that maxContextDepth+1 stays below maxRedactDepth+1, or raise\n"+
			"maxRedactDepth to at least %d. Do not delete this check — the two constants live on\n"+
			"different axes and nothing else keeps them consistent.",
			rootDepth(maxContextDepth)+1, maxRedactDepth+1,
			maxContextDepth, maxRedactDepth, truncatedPlaceholder,
			rootDepth(maxContextDepth)+1)
	}
}

// nestedGroupAttr returns one attr that nests leafKey under `groups` slog group
// levels named g1..gN, so the leaf value lands at ctxLevel groups+1 and, once
// normalized, at rootDepth groups+2.
//
// It is distinct from redact_test.go's nestedContext, which builds an already
// converted `context` map: this one builds the slog attrs a call site would
// actually emit, which is what exercises the conversion cap on its own axis.
func nestedGroupAttr(groups int, leafKey, leafValue string) slog.Attr {
	attrs := []slog.Attr{slog.String(leafKey, leafValue)}
	for i := groups; i >= 1; i-- {
		attrs = []slog.Attr{{Key: fmt.Sprintf("g%d", i), Value: slog.GroupValue(attrs...)}}
	}
	return attrs[0]
}

// TestConversionAtContextCapSurvivesRedaction is the behavioural corroboration
// that the two caps are wired to the correct origins, and that the headroom the
// guard above asserts on constants is real end to end.
//
// A record nested at exactly ctxLevel maxContextDepth is the worst case the
// conversion path can hand to the Redactor. Its deepest value must survive both
// stages: retained by conversion with truncated: false, and then left alone by
// redaction rather than replaced with [TRUNCATED]. If the redaction cap were
// mistakenly applied on the ctxLevel axis — or if the +1 normalization were
// dropped — this is the case that would break, one level before the constants
// look wrong.
func TestConversionAtContextCapSurvivesRedaction(t *testing.T) {
	// A leaf at ctxLevel maxContextDepth needs maxContextDepth-1 enclosing
	// groups, since the outermost group is itself a direct child of `context`.
	groups := int(maxContextDepth) - int(rootContextLevel)

	r := newTestRecord(slog.LevelError, "m")
	r.AddAttrs(nestedGroupAttr(groups, "leaf", "value"))

	ev := convertForTest(t, r)

	if ev.Truncated {
		t.Fatalf("Truncated = true for a leaf at ctxLevel %d, want false: that level is exactly "+
			"the conversion cap and must be retained (Req 3.9)", maxContextDepth)
	}
	if got, want := deepestLeaf(t, ev, groups), "value"; got != want {
		t.Fatalf("deepest converted value = %#v, want %q", got, want)
	}

	// The same event through the Redactor. rootDepth of the leaf is
	// maxContextDepth+1, which the guard above pins strictly above the
	// redaction cap's replacement threshold, so nothing here may change.
	NewRedactor("").Apply(ev)

	if ev.Truncated {
		t.Errorf("Truncated = true after redaction, want false: redaction must not truncate a "+
			"value conversion retained (leaf at rootDepth %d, replacement starts at %d)",
			rootDepth(maxContextDepth)+1, maxRedactDepth+1)
	}
	if containsPlaceholder(ev.Context, truncatedPlaceholder) {
		t.Errorf("context = %#v, want no %q: the redaction cap is unreachable on the conversion path",
			ev.Context, truncatedPlaceholder)
	}
	if got, want := deepestLeaf(t, ev, groups), "value"; got != want {
		t.Errorf("deepest value after redaction = %#v, want %q", got, want)
	}
}

// TestRedactionDepthCapIsLiveForHandBuiltEvents shows the redaction cap is a
// working backstop rather than dead code.
//
// Because conversion bounds `context` first, nothing on that path can reach the
// cap — which is exactly why it needs its own coverage. An Event built by hand
// and passed straight to the Redactor is the isolation-test path the design
// keeps the cap for (Req 12.9), alongside cyclic graphs and any future
// top-level subtree added without a conversion-side cap.
//
// TestRedactorDepthCap pins the boundary at rootDepth maxRedactDepth+1. This
// test pins the two cases that matter for the relationship: an event at the
// deepest rootDepth conversion can emit is untouched, and an event well past
// the cap is replaced.
func TestRedactionDepthCapIsLiveForHandBuiltEvents(t *testing.T) {
	r := NewRedactor("")

	// nestedContext(n) places its leaf at rootDepth n+1, so this is a hand-built
	// event at exactly the depth the conversion path's worst case produces.
	atConversionDepth := &Event{Context: nestedContext(int(maxContextDepth))}
	r.Apply(atConversionDepth)

	if atConversionDepth.Truncated {
		t.Errorf("Truncated = true for a hand-built value at rootDepth %d, want false: the "+
			"conversion path's deepest output must pass redaction untouched",
			rootDepth(maxContextDepth)+1)
	}
	if containsPlaceholder(atConversionDepth.Context, truncatedPlaceholder) {
		t.Errorf("context = %#v, want no %q at rootDepth %d",
			atConversionDepth.Context, truncatedPlaceholder, rootDepth(maxContextDepth)+1)
	}

	// Well past the cap: a depth no conversion output can reach, which only a
	// hand-built or cyclic event produces. The backstop must fire.
	pastCap := &Event{Context: nestedContext(int(maxRedactDepth) + 3)}
	r.Apply(pastCap)

	if !pastCap.Truncated {
		t.Errorf("Truncated = false for a hand-built event nesting past rootDepth %d, want true (Req 10.10)",
			maxRedactDepth)
	}
	if !containsPlaceholder(pastCap.Context, truncatedPlaceholder) {
		t.Errorf("context = %#v, want a %q placeholder: the depth cap is the only termination "+
			"guarantee the Redactor's walk has", pastCap.Context, truncatedPlaceholder)
	}
}

// deepestLeaf walks the marshalled `context` object down through g1..gN and
// returns the value of its `leaf` key, so assertions run against the JSON that
// would go on the wire rather than against the in-memory tree.
func deepestLeaf(t *testing.T, ev *Event, groups int) any {
	t.Helper()

	level := contextOf(t, ev)
	for i := 1; i <= groups; i++ {
		next, ok := level[fmt.Sprintf("g%d", i)].(map[string]any)
		if !ok {
			t.Fatalf("context.g%d is not an object: %#v", i, level)
		}
		level = next
	}
	value, present := level["leaf"]
	if !present {
		t.Fatalf("deepest object = %#v, want a `leaf` key", level)
	}
	return value
}
