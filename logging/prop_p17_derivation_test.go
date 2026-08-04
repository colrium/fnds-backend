package logging

// Feature: betterstack-error-logging — Property 17: Handler derivation places
// attributes at the group path open when they were added.
//
// *For any* sequence of `WithAttrs` and `WithGroup` operations applied to a
// Better_Stack_Handler, the `context` object of the emitted Log_Event equals the
// tree produced by a reference model that nests each attribute batch under the
// group path open at the moment that batch was added, and record attributes
// under the group path open at `Handle` time; and *for any* two handlers derived
// independently from the same parent handler, the Log_Event emitted by each
// contains exactly its own attributes, with none from its sibling.
//
// **Validates: Requirements 3.9, 3.10, 3.11**
//
// Why this property exists at all. No property in the requirements document
// exercised WithAttrs/WithGroup derivation, which is the highest-risk
// correctness area in a custom slog.Handler, and whose worst failure mode —
// sibling handlers appending into one shared backing array — is *intermittent*.
// It appears only when the parent's frames slice happens to carry spare
// capacity, so an example-based test written against a parent built through
// public derivation can never see it: slices.Clip makes spare capacity
// unreachable that way. Randomized interleavings plus a hand-built parent with
// deliberate spare capacity is what turns "intermittent" into "reproducible from
// a seed".
//
// Three claims are asserted, one per test in this file:
//
//  1. TestProperty17DerivedContextMatchesTheReferenceNester — the tree claim,
//     over generated op sequences.
//  2. TestProperty17SiblingHandlersEmitOnlyTheirOwnAttrs — the sibling claim,
//     over parents built with spare capacity and deeper chains than the
//     hand-written example in handler_test.go reaches.
//  3. TestProperty17AttrBatchesBindToTheGroupOpenWhenTheyWereAdded — the
//     h.WithAttrs(a).WithGroup("g").WithAttrs(b) shape, with generated keys,
//     group names, values, and an optional generated enclosing group, rather
//     than the single hand-written case.
//
// Observation point: the **enqueued Log_Event**, drained from the Event_Queue
// after Handle. Asserting on the handler's internal frames — which is what the
// examples in handler_test.go do, and rightly, since they pin the clip
// discipline itself — would leave the binding between a frame's recorded group
// path and the JSON object it lands in untested. The whole point of Req 3.9 is
// where the attribute ends up in the payload.
//
// What the reference nester reimplements, and what it delegates. It
// reimplements every *structural* rule, independently of convert.go and without
// calling appendAttrs: per-frame group binding, record attrs under the
// Handle-time group path, unnamed-group inlining, empty-group dropping, group
// merging when a name is opened twice, last-key-wins at one object level, and
// the ctxLevel 8 depth cap. It delegates only leaf rendering — convertValue,
// sanitizeString, errorFromValue — because rendering one scalar is not what this
// property is about, it is what Properties 2 and 3 are about, and reproducing
// the json.Marshal round trip here would test encoding/json twice while adding a
// second place for the payload shape to drift. The algorithm is deliberately a
// different shape from appendAttrs's: placements are flattened to (group path,
// key, value) triples first and the tree is built from those paths afterwards,
// so "the group path open when the batch was added" is a value this file can
// print in a counterexample rather than an emergent property of a recursion.
//
// Two post-conversion stages run between convertRecord and the queue, and each
// is handled explicitly rather than ignored:
//
//   - **Redaction.** It rewrites `context` before enqueue (Req 10.9), so the
//     reference tree is passed through the same Redactor. That is exact, not
//     approximate: Apply's effect on `context` is a pure function of `context`
//     and the Source_Token, and it is independently pinned by Properties 4 and
//     5, so reusing it as a shared post-stage keeps this file about placement.
//   - **The 64 KiB size cap.** It can shorten context strings and even delete
//     whole context entries, and its behaviour depends on the byte length of the
//     *whole* event, which a context-only reference cannot state. So it is kept
//     inert by construction: the generators run with hazardLargeStrings cleared,
//     which is the only hazard that produces a string big enough to reach the
//     cap. p17Emit asserts the cap really did stay inert, so a future generator
//     change fails here with an explanation instead of producing mystifying
//     mismatches. Oversized events are Property 8's subject.
//
// Every other hazard the harness offers is kept: nil and typed-nil values,
// invalid UTF-8 in keys and values, empty keys, the zero Attr, sensitive keys
// that make redaction actually fire, nested groups, error chains, and
// LogValuers. That is genHandlerOps's documented default minus one flag.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// p17Hazards is the harness default for derivation sequences with
// hazardLargeStrings cleared. See the size-cap note in the file header for why
// that one flag is dropped and why the rest are kept.
const p17Hazards = hazardsSerializable &^ hazardLargeStrings

// ---------------------------------------------------------------------------
// Claim 1: the emitted context equals the reference nester's tree
// ---------------------------------------------------------------------------

func TestProperty17DerivedContextMatchesTheReferenceNester(t *testing.T) {
	r, seed := pbtRand(t)
	shipper, diagnostics := newHandleTestShipper(t, 4)

	// Anti-vacuity counters. "The emitted context equals the reference tree" is
	// satisfied trivially when every generated sequence is a no-op and every
	// event carries no context at all, so the run has to prove it exercised the
	// interleaving it claims to test.
	var (
		groupDraws    int // sequences that opened at least one group
		attrDraws     int // sequences that added at least one attr batch
		bothDraws     int // sequences that did both
		nestedDraws   int // sequences placing an attr under a non-empty group path
		splitDraws    int // sequences placing attrs both at the root and under a group
		depthCapDraws int // sequences whose deepest attrs were cut by the ctxLevel 8 cap
		liftedDraws   int // sequences carrying an error attr onto the top-level object
		contextDraws  int // sequences producing a non-empty context object
	)

	for i := 0; i < pbtIterations; i++ {
		ops := genHandlerOpsWith(r, p17Hazards)

		record := newTestRecord(slog.LevelError, genString(r, p17Hazards))
		record.AddAttrs(genAttrsWith(r, 1+r.Intn(2), p17Hazards, r.Intn(4))...)
		if pbtChance(r, 40) {
			// genHandlerOpsWith draws at most six operations at an attr nesting
			// budget of two, so the ctxLevel 8 cap is out of its reach: without
			// this, no generated event would ever cross it and the cap would be
			// tested only by the hand-written table below. genNestedGroupAttr
			// takes an *exact* depth and genDepth samples 8/9/10/11, which is
			// one level either side of the cap plus two beyond it.
			record.AddAttrs(genNestedGroupAttr(r, genDepth(r), p17Hazards))
		}
		// Read the attrs back off the record rather than reusing the generated
		// slice: slog.Record is free to normalize what it stores, and the model
		// must be fed exactly what convertRecord will iterate.
		recordAttrs := p17RecordAttrs(record)

		derived := p17Fold(NewHandler(shipper, slog.LevelError), ops)
		got := p17Emit(t, shipper, derived, record, seed, i, ops)
		if got == nil {
			return
		}

		want := p17Expect(p17Batches(nil, ops, recordAttrs), shipper.cfg.SourceToken)

		if !reflect.DeepEqual(got.Context, want.context) {
			pbtFailf(t, seed, i, ops,
				"the emitted `context` does not match the reference nester (Req 3.9, 3.10, 3.11)\n"+
					"record attrs:\n%s\nwant context:\n%s\ngot context:\n%s\nplacements:\n%s",
				pbtInputJSON(recordAttrs), pbtInputJSON(want.context),
				pbtInputJSON(got.Context), want.describePlacements())
			continue
		}

		// The depth cap latches `truncated` when it discards something that
		// would have been emitted (Req 3.9). The implication is asserted in one
		// direction only, because `truncated` is a latch the error-chain cap and
		// the size cap also set, so the reverse would be a claim about those.
		if want.dropped > 0 && !got.Truncated {
			pbtFailf(t, seed, i, ops,
				"truncated = false although %d attr(s) sat deeper than ctxLevel %d and were omitted (Req 3.9)",
				want.dropped, maxContextDepth)
		}

		// Traversal-order agreement, which is what decides *which* error attr is
		// lifted out of `context` onto the top-level object (Req 3.6). If the
		// model walked the batches in a different order it could agree on the
		// tree while disagreeing here, so the two are checked together.
		if lifted := got.Error != nil; lifted != want.lifted {
			pbtFailf(t, seed, i, ops,
				"the event carries a top-level `error` object = %t, but the reference model lifted an "+
					"error attr = %t: the model and the handler disagree about traversal order",
				lifted, want.lifted)
		}

		// Counted from the operation sequence, not from the derived batches, so
		// the guard says what it claims: the *sequence* contained a WithGroup
		// that was not the documented no-op and a WithAttrs that was not either.
		openedGroup, addedAttrs := p17OpensAGroup(ops), p17AddsAttrs(ops)
		if openedGroup {
			groupDraws++
		}
		if addedAttrs {
			attrDraws++
		}
		if openedGroup && addedAttrs {
			bothDraws++
		}
		if want.nested {
			nestedDraws++
		}
		if want.nested && want.atRoot {
			splitDraws++
		}
		if want.dropped > 0 {
			depthCapDraws++
		}
		if want.lifted {
			liftedDraws++
		}
		if len(want.context) > 0 {
			contextDraws++
		}
	}

	if diagnostics.Len() != 0 {
		t.Errorf("Self_Diagnostics were written for well-behaved generated input, want none:\n%s",
			diagnostics.String())
	}

	// Without these, a Handler whose WithAttrs and WithGroup both returned the
	// receiver unchanged would pass every assertion above.
	if bothDraws == 0 {
		t.Errorf("no generated sequence both opened a group and added an attr batch in %d draws "+
			"(seed %d): groups in %d draws, attr batches in %d; the run was all no-ops and the "+
			"property held vacuously", pbtIterations, seed, groupDraws, attrDraws)
	}
	if contextDraws == 0 {
		t.Errorf("no generated sequence produced a non-empty `context` object in %d draws (seed %d); "+
			"comparing two empty trees proves nothing", pbtIterations, seed)
	}
	if nestedDraws == 0 {
		t.Errorf("no generated sequence placed an attr under a non-empty group path in %d draws "+
			"(seed %d); the group binding of Req 3.9 went untested", pbtIterations, seed)
	}
	if depthCapDraws == 0 {
		t.Errorf("no generated sequence placed an attr deeper than ctxLevel %d in %d draws (seed %d); "+
			"the depth cap of Req 3.9 went untested by generated input",
			maxContextDepth, pbtIterations, seed)
	}
	if splitDraws == 0 {
		t.Errorf("no generated sequence placed attrs both at the root of `context` and under a group "+
			"in %d draws (seed %d); the batch-binds-to-the-groups-open-when-added rule is only "+
			"distinguishable from a flat attr list when both occur in one event", pbtIterations, seed)
	}
	t.Logf("coverage over %d draws: groups %d, attr batches %d, both %d, nested %d, root+nested %d, "+
		"depth cap %d, lifted error %d, non-empty context %d",
		pbtIterations, groupDraws, attrDraws, bothDraws, nestedDraws, splitDraws,
		depthCapDraws, liftedDraws, contextDraws)
}

// ---------------------------------------------------------------------------
// Claim 2: siblings derived from one parent emit only their own attrs
// ---------------------------------------------------------------------------

func TestProperty17SiblingHandlersEmitOnlyTheirOwnAttrs(t *testing.T) {
	r, seed := pbtRand(t)
	shipper, _ := newHandleTestShipper(t, 4)

	var (
		spareFrameCap int // parents whose frames slice really had room to append into
		deepParents   int // parents with at least two groups already open
		groupedBranch int // iterations where at least one branch opened a group
	)

	for i := 0; i < pbtIterations; i++ {
		parent, parentBatches, parentGroups := p17SpareCapacityParent(r, shipper)
		leftOps := p17MarkedOps(r, "p17left")
		rightOps := p17MarkedOps(r, "p17right")

		// Both derivations happen before either handler emits, which is the
		// ordering that exposes aliasing: with the classic
		// `append(h.frames, f)` bug, the second derivation overwrites the
		// first's frame in the shared array, and the first handler then ships
		// its sibling's attrs. Deriving-and-emitting one at a time would hide it.
		left := p17Fold(parent, leftOps)
		right := p17Fold(parent, rightOps)

		branches := []struct {
			name    string
			handler slog.Handler
			ops     []handlerOp
			foreign []string
		}{
			{"left", left, leftOps, []string{"p17right"}},
			{"right", right, rightOps, []string{"p17left"}},
			// The parent emits last, after both derivations have run, so a
			// derivation that wrote through to its parent is caught too. Neither
			// branch's marker may appear in it.
			{"parent", parent, nil, []string{"p17left", "p17right"}},
		}

		failed := false
		for _, branch := range branches {
			record := newTestRecord(slog.LevelError, "sibling derivation")
			record.AddAttrs(slog.String("p17rec_"+branch.name, genString(r, hazardNone)))

			got := p17Emit(t, shipper, branch.handler, record, seed, i, branch.ops)
			if got == nil {
				return
			}

			want := p17Expect(
				p17Batches(parentGroups, branch.ops, p17RecordAttrs(record)).prepend(parentBatches),
				shipper.cfg.SourceToken,
			)

			// Checked before the tree comparison, and stated separately from it,
			// because this is the claim the property is named for. Aliasing shows
			// up in the tree comparison too, but only as an unattributed "trees
			// differ"; the marker prefixes let the failure name the sibling the
			// attribute was stolen from.
			leaks := false
			for _, prefix := range branch.foreign {
				leaked := p17KeysWithPrefix(got.Context, prefix)
				if len(leaked) == 0 {
					continue
				}
				pbtFailf(t, seed, i, branch.ops,
					"the %s handler shipped %v, which belong to the %s derivation: two handlers "+
						"derived from one parent are sharing a backing array\ncontext:\n%s",
					branch.name, leaked, prefix, pbtInputJSON(got.Context))
				failed, leaks = true, true
			}
			if leaks {
				continue
			}

			if !reflect.DeepEqual(got.Context, want.context) {
				pbtFailf(t, seed, i, branch.ops,
					"the %s handler's `context` does not match the reference nester\n"+
						"parent groups: %v\nparent batches:\n%s\nwant:\n%s\ngot:\n%s",
					branch.name, parent.groups, p17DescribeBatches(parentBatches),
					pbtInputJSON(want.context), pbtInputJSON(got.Context))
				failed = true
			}
		}
		if failed {
			continue
		}

		if cap(parent.frames) > len(parent.frames) {
			spareFrameCap++
		}
		if len(parent.groups) > 1 {
			deepParents++
		}
		if p17OpensAGroup(leftOps) || p17OpensAGroup(rightOps) {
			groupedBranch++
		}
	}

	// The aliasing hazard only exists when the parent has room to append into,
	// so a run of parents without spare capacity would pass for the wrong reason.
	if spareFrameCap == 0 {
		t.Errorf("no generated parent had spare frames capacity in %d draws (seed %d); the aliasing "+
			"precondition was never present, so sibling disjointness held vacuously",
			pbtIterations, seed)
	}
	if deepParents == 0 {
		t.Errorf("no generated parent had more than one group already open in %d draws (seed %d); "+
			"the chain never got deeper than the hand-written example in handler_test.go",
			pbtIterations, seed)
	}
	if groupedBranch == 0 {
		t.Errorf("no branch opened a group of its own in %d draws (seed %d); WithGroup's clip of the "+
			"frames slice went untested", pbtIterations, seed)
	}
	t.Logf("coverage over %d draws: parents with spare frames capacity %d, parents deeper than one "+
		"group %d, iterations with a branch group %d", pbtIterations, spareFrameCap, deepParents, groupedBranch)
}

// ---------------------------------------------------------------------------
// Claim 3: a batch binds to the groups open when it was added
// ---------------------------------------------------------------------------

func TestProperty17AttrBatchesBindToTheGroupOpenWhenTheyWereAdded(t *testing.T) {
	// h.WithAttrs(a).WithGroup("g").WithAttrs(b) must place a at context.a and b
	// at context.g.b. handler_test.go pins the single hand-written case; here the
	// two keys, the group name, both values, and an optional enclosing group are
	// generated, so the invariant is asserted over the shape rather than over one
	// instance of it. A handler that stored a flat attr list and applied its
	// currently open groups at Handle time puts *both* keys under g and fails
	// every draw.
	r, seed := pbtRand(t)
	shipper, _ := newHandleTestShipper(t, 4)

	wrappedDraws := 0

	for i := 0; i < pbtIterations; i++ {
		keyA := pbtPick(r, pbtBenignKeys)
		keyB := pbtPick(r, pbtBenignKeys)
		for keyB == keyA {
			keyB = pbtPick(r, pbtBenignKeys)
		}
		keyC := pbtPick(r, pbtBenignKeys)
		for keyC == keyA || keyC == keyB {
			keyC = pbtPick(r, pbtBenignKeys)
		}
		// Drawn from a dedicated pool so the group name cannot collide with a
		// generated attr key, which would make the expected placement ambiguous
		// by last-key-wins rather than wrong.
		group := "p17g" + strconv.Itoa(r.Intn(3))
		outer := ""
		if pbtChance(r, 50) {
			outer = "p17outer" + strconv.Itoa(r.Intn(2))
			wrappedDraws++
		}

		valueA, valueB, valueC := genString(r, hazardNone), genString(r, hazardNone), genString(r, hazardNone)

		derived := slog.Handler(NewHandler(shipper, slog.LevelError))
		if outer != "" {
			derived = derived.WithGroup(outer)
		}
		derived = derived.
			WithAttrs([]slog.Attr{slog.String(keyA, valueA)}).
			WithGroup(group).
			WithAttrs([]slog.Attr{slog.String(keyB, valueB)})

		record := newTestRecord(slog.LevelError, "binding")
		record.AddAttrs(slog.String(keyC, valueC))

		input := map[string]any{
			"outer": outer, "group": group,
			"keyA": keyA, "keyB": keyB, "keyC": keyC,
		}
		got := p17Emit(t, shipper, derived, record, seed, i, input)
		if got == nil {
			return
		}

		// The batch added before any group was opened stays at the level where
		// it was added, even though a group is open by the time Handle runs.
		base := got.Context
		if outer != "" {
			nested, ok := base[outer].(map[string]any)
			if !ok {
				pbtFailf(t, seed, i, input,
					"context[%q] is %s, want the enclosing group object\ncontext:\n%s",
					outer, p17DescribeValue(base[outer]), pbtInputJSON(got.Context))
				continue
			}
			base = nested
		}

		if base[keyA] != valueA {
			pbtFailf(t, seed, i, input,
				"the first batch landed as [%q] = %s, want %s at the level where it was added "+
					"(Req 3.9)\ncontext:\n%s",
				keyA, p17DescribeValue(base[keyA]), pbtSafeString(valueA), pbtInputJSON(got.Context))
			continue
		}

		inner, ok := base[group].(map[string]any)
		if !ok {
			pbtFailf(t, seed, i, input,
				"[%q] is %s, want the group object holding the second batch\ncontext:\n%s",
				group, p17DescribeValue(base[group]), pbtInputJSON(got.Context))
			continue
		}
		if inner[keyB] != valueB {
			pbtFailf(t, seed, i, input,
				"the second batch landed as [%q][%q] = %s, want %s: a batch nests under the groups "+
					"open when it was added (Req 3.9)\ncontext:\n%s",
				group, keyB, p17DescribeValue(inner[keyB]), pbtSafeString(valueB),
				pbtInputJSON(got.Context))
			continue
		}
		// Record attrs nest under the groups open at Handle time (Req 3.9),
		// which here is the innermost group.
		if inner[keyC] != valueC {
			pbtFailf(t, seed, i, input,
				"the record attr landed as [%q][%q] = %s, want %s under the groups open at Handle "+
					"time\ncontext:\n%s",
				group, keyC, p17DescribeValue(inner[keyC]), pbtSafeString(valueC),
				pbtInputJSON(got.Context))
			continue
		}

		// The half that fails when a handler applies the Handle-time groups to
		// every batch: keyA would appear inside the group and not outside it.
		if _, present := inner[keyA]; present {
			pbtFailf(t, seed, i, input,
				"[%q][%q] is present: the first batch was nested under a group that was opened "+
					"after it (Req 3.9)\ncontext:\n%s", group, keyA, pbtInputJSON(got.Context))
			continue
		}
		if _, present := base[keyB]; present {
			pbtFailf(t, seed, i, input,
				"[%q] is present at the level above [%q]: the second batch ignored the open group "+
					"(Req 3.9)\ncontext:\n%s", keyB, group, pbtInputJSON(got.Context))
		}
	}

	if wrappedDraws == 0 {
		t.Errorf("no draw wrapped the shape in an enclosing group in %d draws (seed %d)",
			pbtIterations, seed)
	}
}

// ---------------------------------------------------------------------------
// The reference nester
// ---------------------------------------------------------------------------

// p17Batch is one attribute batch together with the group path that was open
// when it was added. It is the model's counterpart to attrFrame, declared
// separately so the model cannot accidentally read the handler's own state: a
// p17Batch is derived from the *operation sequence*, never from h.frames.
type p17Batch struct {
	groups []string
	attrs  []slog.Attr
}

// p17BatchList is the ordered batch list for one event: the derivation batches
// followed by the record's own attrs bound to the Handle-time group path.
type p17BatchList []p17Batch

// prepend returns base followed by list, which is how a hand-built parent's
// pre-existing frames are folded into the expectation for a derived handler.
func (list p17BatchList) prepend(base p17BatchList) p17BatchList {
	combined := make(p17BatchList, 0, len(base)+len(list))
	combined = append(combined, base...)
	return append(combined, list...)
}

// p17Batches simulates a WithAttrs/WithGroup sequence starting from a handler
// that already has open groups, and appends the record's own attrs under the
// group path left open at the end.
//
// open is nil for a sequence applied to a root handler. It is non-nil only for
// the sibling case, where the parent is built by hand with groups already open;
// that value comes from the test's own construction, never from reading the
// handler's state, so the model stays independent of what it is checking.
//
// The two documented contract no-ops are honoured here rather than relied on:
// WithGroup("") must leave the handler unchanged, and WithAttrs on an empty
// batch may. Note the group path is copied on every extension — the model is
// deliberately built the way the handler has to be, so that a snapshot taken for
// one batch cannot be mutated by a later operation. That is the same discipline
// slices.Clip enforces in WithAttrs, restated independently.
func p17Batches(open []string, ops []handlerOp, recordAttrs []slog.Attr) p17BatchList {
	var batches p17BatchList
	open = p17CloneStrings(open)

	for _, op := range ops {
		if op.Kind == handlerOpGroup {
			if op.Group == "" {
				continue // WithGroup("") returns the receiver
			}
			extended := make([]string, 0, len(open)+1)
			extended = append(extended, open...)
			open = append(extended, op.Group)
			continue
		}
		if len(op.Attrs) == 0 {
			continue // WithAttrs(nil) returns the receiver
		}
		batches = append(batches, p17Batch{groups: p17CloneStrings(open), attrs: op.Attrs})
	}
	if len(recordAttrs) > 0 {
		batches = append(batches, p17Batch{groups: p17CloneStrings(open), attrs: recordAttrs})
	}
	return batches
}

// p17Placement is one leaf that the model expects to find in `context`: the
// named group path it sits under, its key, and its resolved value.
//
// Flattening to placements first is what makes the model independent of
// appendAttrs rather than a paraphrase of it, and it is what lets a
// counterexample print the group path the model expected each attribute to be
// bound to.
type p17Placement struct {
	path  []string
	key   string
	value slog.Value
}

// p17Expectation is everything the model predicts about one emitted event.
type p17Expectation struct {
	// context is the expected `context` object *after* redaction, or nil when
	// no attribute survived, which is how conversion represents "no context".
	context map[string]any
	// placements is carried for counterexample reporting: it is the model's
	// answer to "which group path did you expect this attribute to bind to".
	placements []p17Placement
	// dropped counts leaves omitted by the ctxLevel 8 cap (Req 3.9).
	dropped int
	// lifted records that an error-valued attr became the top-level `error`
	// object instead of a context entry (Req 3.6).
	lifted bool
	// nested and atRoot feed the anti-vacuity guards: an event with attrs both
	// under a group and at the root is the only shape that distinguishes
	// per-batch binding from a flat attr list.
	nested bool
	atRoot bool
}

func (e p17Expectation) describePlacements() string {
	described := make([]any, 0, len(e.placements))
	for _, placement := range e.placements {
		described = append(described, map[string]any{
			"groups": placement.path,
			"key":    pbtSafeString(placement.key),
			"value":  pbtDescribeValue(placement.value),
		})
	}
	return pbtInputJSON(described)
}

// p17Expect runs the whole model: flatten the batches to placements, build the
// tree from their paths, then apply the one post-conversion stage that rewrites
// `context` before it reaches the queue.
func p17Expect(batches p17BatchList, sourceToken string) p17Expectation {
	var expectation p17Expectation
	for _, batch := range batches {
		expectation.flatten(batch.attrs, batch.groups)
	}
	expectation.build()

	// Redaction runs strictly before enqueue (Req 10.9), so the queue holds a
	// redacted event and the expectation has to be redacted too. Its effect on
	// `context` depends on nothing but `context` and the token, so reusing the
	// production Redactor here is exact rather than approximate; what it does is
	// Properties 4 and 5's subject, not this file's.
	if expectation.context != nil {
		scratch := &Event{Context: expectation.context}
		NewRedactor(sourceToken).Apply(scratch)
		expectation.context = scratch.Context
	}
	return expectation
}

// flatten walks one batch's attrs, recording where each leaf belongs.
//
// path is the named group chain enclosing attrs, so an attr in this list sits at
// ctxLevel len(path)+1. Every rule applied here is from the documented
// slog.Handler contract or from Requirement 3:
//
//   - the zero Attr is skipped;
//   - Resolve runs before the value is classified, so a LogValuer's replacement
//     is what gets placed (Req 10.8);
//   - a group with no attrs is dropped, key and all;
//   - a group with an empty key is inlined, consuming no depth;
//   - a leaf deeper than ctxLevel 8 is omitted and counted (Req 3.9).
//
// The cap is evaluated per leaf rather than per level, which is why a group
// beyond the cap is still descended into: its leaves are deeper still and are
// omitted individually. The set of surviving leaves is identical either way, and
// only whether anything was dropped is used, never how many.
func (e *p17Expectation) flatten(attrs []slog.Attr, path []string) {
	for _, a := range attrs {
		if a.Equal(slog.Attr{}) {
			continue
		}
		value := a.Value.Resolve()

		if value.Kind() == slog.KindGroup {
			group := value.Group()
			if len(group) == 0 {
				continue
			}
			if a.Key == "" {
				e.flatten(group, path)
				continue
			}
			extended := make([]string, 0, len(path)+1)
			extended = append(extended, path...)
			e.flatten(group, append(extended, sanitizeString(a.Key)))
			continue
		}

		if ctxLevel(len(path)+1) > maxContextDepth {
			e.dropped++
			continue
		}
		e.placements = append(e.placements, p17Placement{
			path: path, key: sanitizeString(a.Key), value: value,
		})
		if len(path) > 0 {
			e.nested = true
		} else {
			e.atRoot = true
		}
	}
}

// build assembles the expected tree from the flattened placements, in the order
// they were collected.
//
// Group objects are created lazily, on the way to a leaf that is actually going
// to be written. That is what reproduces two separate rules with one mechanism:
// a group whose every attribute was dropped — by the depth cap, or by being
// lifted onto the top-level error object — leaves no empty object behind, and a
// group name opened twice merges into the object already at that key instead of
// replacing it (Req 3.11). Writing in placement order is what gives
// last-key-wins at each level, including a scalar that overwrites an earlier
// group of the same name and vice versa.
func (e *p17Expectation) build() {
	root := make(map[string]any)
	// convertValue takes an Event only so a future caller can latch truncated on
	// it; the scratch keeps this model from being handed a nil.
	scratch := new(Event)

	for _, placement := range e.placements {
		var rendered any
		if err, ok := errorFromValue(placement.value); ok {
			if !e.lifted {
				// The first error-valued attr in traversal order becomes the
				// top-level `error` object and is not repeated under `context`
				// (Req 3.6), so it contributes no group objects either.
				e.lifted = true
				continue
			}
			rendered = sanitizeString(err.Error())
		} else {
			rendered = convertValue(placement.value, scratch)
		}

		node := root
		for _, segment := range placement.path {
			child, ok := node[segment].(map[string]any)
			if !ok {
				child = make(map[string]any)
				node[segment] = child
			}
			node = child
		}
		node[placement.key] = rendered
	}

	if len(root) == 0 {
		// Conversion omits `context` entirely when nothing survived, so "empty"
		// has exactly one representation and DeepEqual is meaningful.
		return
	}
	e.context = root
}

// ---------------------------------------------------------------------------
// Emission and small helpers
// ---------------------------------------------------------------------------

// p17Fold applies a derivation sequence and returns the resulting handler.
func p17Fold(h slog.Handler, ops []handlerOp) slog.Handler {
	for _, op := range ops {
		h = op.apply(h)
	}
	return h
}

// p17OpensAGroup reports whether a sequence contains a WithGroup that is not the
// documented no-op.
func p17OpensAGroup(ops []handlerOp) bool {
	for _, op := range ops {
		if op.Kind == handlerOpGroup && op.Group != "" {
			return true
		}
	}
	return false
}

// p17AddsAttrs reports whether a sequence contains a WithAttrs carrying a
// non-empty batch, the empty batch being the other documented no-op.
func p17AddsAttrs(ops []handlerOp) bool {
	for _, op := range ops {
		if op.Kind == handlerOpAttrs && len(op.Attrs) > 0 {
			return true
		}
	}
	return false
}

// p17RecordAttrs reads a record's attrs back out in iteration order, which is
// the order convertRecord sees them in.
func p17RecordAttrs(record slog.Record) []slog.Attr {
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	return attrs
}

func p17CloneStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	cloned := make([]string, len(s))
	copy(cloned, s)
	return cloned
}

// p17Emit hands one record to a handler and returns the single event that
// reached the Event_Queue, draining the queue so the next iteration starts empty.
//
// It also asserts the two things this property needs to be true of the pipeline
// rather than of the handler, and which would otherwise turn into unexplained
// tree mismatches:
//
//   - exactly one event was admitted and enqueued, so a recovered conversion
//     panic or a dropped event is reported as itself;
//   - the 64 KiB size cap stayed inert. Its own truncation indicator in the
//     payload, or a payload anywhere near the cap, means the generators started
//     producing oversized events and the reasoning in the file header no longer
//     holds.
//
// It returns nil only after failing the test, so a caller may return immediately.
func p17Emit(
	t *testing.T, s *Shipper, h slog.Handler, record slog.Record,
	seed int64, iteration int, input any,
) *Event {
	t.Helper()

	before := s.counters.snapshot()
	if err := h.Handle(context.Background(), record); err != nil {
		pbtFatalf(t, seed, iteration, input, "Handle returned %v, want nil", err)
		return nil
	}

	events := queuedEvents(s)
	if len(events) != 1 {
		pbtFatalf(t, seed, iteration, input,
			"Handle enqueued %d events, want exactly 1 (counters %+v -> %+v)",
			len(events), before, s.counters.snapshot())
		return nil
	}
	event := events[0]

	encoded, err := json.Marshal(event)
	if err != nil {
		pbtFatalf(t, seed, iteration, input, "the enqueued event does not marshal: %v", err)
		return nil
	}
	if len(encoded) > maxEventBytes/2 || bytes.Contains(encoded, []byte(truncationIndicator)) {
		pbtFatalf(t, seed, iteration, input,
			"the enqueued event serializes to %d bytes, close to or past the %d byte cap: Property 17 "+
				"clears hazardLargeStrings precisely so the size cap cannot rewrite `context` behind "+
				"the reference model. If the generators changed, either restore that exclusion or "+
				"teach the model the size cap (which is Property 8's subject)",
			len(encoded), maxEventBytes)
		return nil
	}
	return event
}

// p17SpareCapacityParent builds a parent handler whose frames and groups slices
// carry spare capacity, and returns the batches that parent represents.
//
// Constructing it directly is the whole point, and it is the same reason
// handler_test.go's sibling example does: the clip discipline means no sequence
// of public WithAttrs/WithGroup calls can produce a handler with spare capacity,
// so an internal parent is the only way to put the aliasing precondition in
// place and prove the discipline is what prevents the bug rather than luck about
// how append happened to size an array.
//
// Two things it does that the hand-written example does not: the group chain goes
// up to three levels deep instead of one, and the base batches are bound to
// *different prefixes* of that chain, so a derivation that corrupted the group
// path would move some base attrs and not others. The prefixes deliberately
// alias the parent's own groups array, which is the most hostile arrangement
// available: a WithGroup that appended in place would be writing next door to
// them.
// It returns the handler, the batches it already carries, and the group path it
// already has open — the last two describing the parent in the model's own terms,
// built from the same literals the handler was built from rather than read back
// off the handler.
func p17SpareCapacityParent(r *rand.Rand, s *Shipper) (*Handler, p17BatchList, []string) {
	depth := r.Intn(4)          // 0..3 groups already open on the parent
	batchCount := 1 + r.Intn(3) // 1..3 batches already recorded
	spare := 1 + r.Intn(4)      // room to append into, which is the hazard

	groups := make([]string, 0, depth+spare)
	for level := 0; level < depth; level++ {
		groups = append(groups, "p17base_g"+strconv.Itoa(level))
	}

	frames := make([]attrFrame, 0, batchCount+spare)
	batches := make(p17BatchList, 0, batchCount)
	for batch := 0; batch < batchCount; batch++ {
		bound := groups[:r.Intn(len(groups)+1)]
		attrs := []slog.Attr{
			slog.String("p17base_a"+strconv.Itoa(batch), genString(r, hazardNone)),
		}
		frames = append(frames, attrFrame{groups: bound, attrs: attrs})
		batches = append(batches, p17Batch{groups: p17CloneStrings(bound), attrs: attrs})
	}

	return &Handler{
		shipper: s,
		level:   slog.LevelError,
		cfg:     newConvertConfig(s.cfg),
		groups:  groups,
		frames:  frames,
	}, batches, p17CloneStrings(groups)
}

// p17MarkedOps returns a derivation sequence whose every key and group name
// carries marker as a prefix, so a leaked attribute can be attributed to the
// sibling it came from by name. At least one attr batch is always produced, or
// "each emits exactly its own attrs" would be a claim about nothing.
func p17MarkedOps(r *rand.Rand, marker string) []handlerOp {
	ops := make([]handlerOp, 0, 5)
	attrOps := 0
	for count := 1 + r.Intn(4); count > 0; count-- {
		if pbtChance(r, 40) {
			ops = append(ops, handlerOp{
				Kind:  handlerOpGroup,
				Group: marker + "_g" + strconv.Itoa(r.Intn(2)),
			})
			continue
		}
		ops = append(ops, handlerOp{Kind: handlerOpAttrs, Attrs: []slog.Attr{
			slog.String(marker+"_a"+strconv.Itoa(attrOps), genString(r, hazardNone)),
		}})
		attrOps++
	}
	if attrOps == 0 {
		ops = append(ops, handlerOp{Kind: handlerOpAttrs, Attrs: []slog.Attr{
			slog.String(marker+"_a0", genString(r, hazardNone)),
		}})
	}
	return ops
}

// p17KeysWithPrefix returns every key at any depth of a context tree that starts
// with prefix, sorted so a failure message is stable across runs of one seed.
func p17KeysWithPrefix(context map[string]any, prefix string) []string {
	found := make(map[string]bool)
	p17WalkKeys(context, func(key string) {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			found[key] = true
		}
	})
	keys := make([]string, 0, len(found))
	for key := range found {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// p17WalkKeys visits every object key of a converted context tree. No depth
// guard is needed: conversion caps nesting at ctxLevel 8 and produces no cycles.
func p17WalkKeys(v any, visit func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for key, child := range t {
			visit(key)
			p17WalkKeys(child, visit)
		}
	case []any:
		for _, child := range t {
			p17WalkKeys(child, visit)
		}
	}
}

// p17DescribeValue renders one context value for a failure message.
func p17DescribeValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "absent or null"
	case string:
		return pbtSafeString(t)
	case map[string]any:
		return fmt.Sprintf("an object with %d keys", len(t))
	case []any:
		return fmt.Sprintf("an array of %d", len(t))
	default:
		return fmt.Sprintf("%v (%T)", t, t)
	}
}

// p17DescribeBatches renders a batch list for a failure message.
func p17DescribeBatches(batches p17BatchList) string {
	described := make([]any, 0, len(batches))
	for _, batch := range batches {
		described = append(described, map[string]any{
			"groups": batch.groups,
			"attrs":  pbtDescribeAttrs(batch.attrs),
		})
	}
	return pbtInputJSON(described)
}

// ---------------------------------------------------------------------------
// Oracle self-tests
// ---------------------------------------------------------------------------

// TestProperty17ReferenceNesterMatchesHandWrittenTrees pins the model against
// trees written out by hand, one per structural rule it claims to reproduce.
//
// The property above is only as good as this: a reference nester that agreed
// with a bug in appendAttrs would make the property pass while the payload was
// wrong, and no amount of random input can reveal that, because both sides would
// move together. Every expectation here is transcribed from the requirements and
// the slog.Handler contract, not from convert.go.
//
// Redaction is not applied in these cases — none of the keys is sensitive — so
// the trees below are the nester's raw output.
func TestProperty17ReferenceNesterMatchesHandWrittenTrees(t *testing.T) {
	// nestedAttr wraps leaf in depth named groups n1..nDepth, outermost first,
	// so the leaf sits at ctxLevel depth+1 when the batch is added at the root.
	nestedAttr := func(depth int, leaf slog.Attr) slog.Attr {
		for level := depth; level > 0; level-- {
			leaf = slog.Attr{Key: "n" + strconv.Itoa(level), Value: slog.GroupValue(leaf)}
		}
		return leaf
	}
	// nestedWant is the object tree nestedAttr is expected to produce.
	nestedWant := func(depth int, key string, value any) map[string]any {
		tree := map[string]any{key: value}
		for level := depth; level > 0; level-- {
			tree = map[string]any{"n" + strconv.Itoa(level): tree}
		}
		return tree
	}
	// groupOps opens count groups named d0..d(count-1) through WithGroup, and
	// groupWant is the tree they are expected to produce around a leaf.
	groupOps := func(count int) []handlerOp {
		ops := make([]handlerOp, 0, count)
		for level := 0; level < count; level++ {
			ops = append(ops, p17GroupOp("d"+strconv.Itoa(level)))
		}
		return ops
	}
	groupWant := func(count int, leaf map[string]any) map[string]any {
		for level := count - 1; level >= 0; level-- {
			leaf = map[string]any{"d" + strconv.Itoa(level): leaf}
		}
		return leaf
	}

	first := fmt.Errorf("p17 first error")
	second := fmt.Errorf("p17 second error")

	cases := []struct {
		name        string
		ops         []handlerOp
		recordAttrs []slog.Attr
		want        map[string]any
		wantDropped bool
		wantLifted  bool
	}{
		{
			// Req 3.9, and the whole reason attrFrame exists.
			name: "a batch binds to the groups open when it was added",
			ops: []handlerOp{
				p17AttrsOp(slog.String("a", "1")),
				p17GroupOp("g"),
				p17AttrsOp(slog.String("b", "2")),
			},
			recordAttrs: []slog.Attr{slog.String("c", "3")},
			want:        map[string]any{"a": "1", "g": map[string]any{"b": "2", "c": "3"}},
		},
		{
			// Required by the slog.Handler contract, not merely permitted.
			name: "an empty group name leaves the path unchanged",
			ops: []handlerOp{
				p17GroupOp(""),
				p17AttrsOp(slog.String("a", "1")),
				p17GroupOp(""),
				p17AttrsOp(slog.String("b", "2")),
			},
			want: map[string]any{"a": "1", "b": "2"},
		},
		{
			name: "an empty attr batch adds nothing and `context` is omitted",
			ops:  []handlerOp{p17AttrsOp(), p17GroupOp("g"), p17AttrsOp()},
			want: nil,
		},
		{
			name: "an unnamed group attr is inlined and consumes no depth",
			ops:  []handlerOp{p17AttrsOp(slog.Group("", slog.String("x", "1")))},
			want: map[string]any{"x": "1"},
		},
		{
			name: "a group with no attrs is dropped, key and all",
			ops:  []handlerOp{p17AttrsOp(slog.Group("empty"), slog.String("k", "v"))},
			want: map[string]any{"k": "v"},
		},
		{
			// Req 3.11: a name opened twice merges, so the earlier batch's attrs
			// survive and only colliding keys are overwritten.
			name: "a group name opened twice merges rather than replacing",
			ops: []handlerOp{
				p17AttrsOp(slog.Group("g", slog.String("x", "1"))),
				p17AttrsOp(slog.Group("g", slog.String("y", "2"))),
			},
			want: map[string]any{"g": map[string]any{"x": "1", "y": "2"}},
		},
		{
			// Req 3.11: the later attr wins and the key appears exactly once.
			name: "a later duplicate key at one object level wins",
			ops: []handlerOp{
				p17AttrsOp(slog.String("k", "first")),
				p17AttrsOp(slog.String("k", "second")),
			},
			recordAttrs: []slog.Attr{slog.String("k", "third")},
			want:        map[string]any{"k": "third"},
		},
		{
			name: "a scalar overwrites an earlier group of the same name",
			ops: []handlerOp{
				p17AttrsOp(slog.Group("g", slog.String("x", "1")), slog.String("g", "flat")),
			},
			want: map[string]any{"g": "flat"},
		},
		{
			name: "a group overwrites an earlier scalar of the same name",
			ops: []handlerOp{
				p17AttrsOp(slog.String("g", "flat"), slog.Group("g", slog.String("x", "1"))),
			},
			want: map[string]any{"g": map[string]any{"x": "1"}},
		},
		{
			name: "the zero Attr is ignored",
			ops:  []handlerOp{p17AttrsOp(slog.Attr{}, slog.String("k", "v"), slog.Attr{})},
			want: map[string]any{"k": "v"},
		},
		{
			// Req 3.10: a key colliding with a reserved top-level name still goes
			// under `context`, and the top-level field is unaffected.
			name: "a reserved top-level key lands under context",
			ops:  []handlerOp{p17AttrsOp(slog.String("level", "shadow"), slog.String("message", "shadow"))},
			want: map[string]any{"level": "shadow", "message": "shadow"},
		},
		{
			name: "an attr at exactly ctxLevel 8 survives",
			ops:  []handlerOp{p17AttrsOp(nestedAttr(int(maxContextDepth)-1, slog.String("k", "v")))},
			want: nestedWant(int(maxContextDepth)-1, "k", "v"),
		},
		{
			// Req 3.9: one level deeper is omitted, and the groups left empty by
			// the omission are dropped rather than emitted as empty objects.
			name:        "an attr at ctxLevel 9 is omitted and its empty groups vanish",
			ops:         []handlerOp{p17AttrsOp(nestedAttr(int(maxContextDepth), slog.String("k", "v")))},
			want:        nil,
			wantDropped: true,
		},
		{
			name: "derivation groups count toward the depth cap",
			ops: append(groupOps(int(maxContextDepth)-1),
				p17AttrsOp(slog.String("k", "v"))),
			want: groupWant(int(maxContextDepth)-1, map[string]any{"k": "v"}),
		},
		{
			name: "an attr under too many derivation groups is omitted",
			ops: append(groupOps(int(maxContextDepth)),
				p17AttrsOp(slog.String("k", "v"))),
			want:        nil,
			wantDropped: true,
		},
		{
			// Req 3.6: the first error-valued attr in traversal order becomes the
			// top-level object; the rest stay under context as their text.
			name: "the first error attr is lifted and later ones stay as text",
			ops: []handlerOp{
				p17AttrsOp(slog.Any("first", first)),
				p17AttrsOp(slog.Any("second", second)),
			},
			want:       map[string]any{"second": second.Error()},
			wantLifted: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			shipper, _ := newHandleTestShipper(t, 2)

			record := newTestRecord(slog.LevelError, "oracle")
			record.AddAttrs(testCase.recordAttrs...)

			got := p17Expect(p17Batches(nil, testCase.ops, p17RecordAttrs(record)), shipper.cfg.SourceToken)

			if !reflect.DeepEqual(got.context, testCase.want) {
				t.Errorf("the reference nester produced\n%s\nwant\n%s\nplacements:\n%s",
					pbtInputJSON(got.context), pbtInputJSON(testCase.want), got.describePlacements())
			}
			if dropped := got.dropped > 0; dropped != testCase.wantDropped {
				t.Errorf("the reference nester dropped %d attrs past the depth cap, want dropped = %t",
					got.dropped, testCase.wantDropped)
			}
			if got.lifted != testCase.wantLifted {
				t.Errorf("the reference nester lifted an error attr = %t, want %t",
					got.lifted, testCase.wantLifted)
			}

			// And the handler must agree with the hand-written tree, which is
			// what makes these cases a check on both sides rather than on the
			// model alone.
			derived := p17Fold(NewHandler(shipper, slog.LevelError), testCase.ops)
			if err := derived.Handle(context.Background(), record); err != nil {
				t.Fatalf("Handle returned %v, want nil", err)
			}
			events := queuedEvents(shipper)
			if len(events) != 1 {
				t.Fatalf("Handle enqueued %d events, want exactly 1", len(events))
			}
			if !reflect.DeepEqual(events[0].Context, testCase.want) {
				t.Errorf("the handler emitted\n%s\nwant\n%s",
					pbtInputJSON(events[0].Context), pbtInputJSON(testCase.want))
			}
			if testCase.wantDropped && !events[0].Truncated {
				t.Error("truncated = false although the depth cap omitted an attr, want true (Req 3.9)")
			}
			if lifted := events[0].Error != nil; lifted != testCase.wantLifted {
				t.Errorf("the handler produced a top-level `error` object = %t, want %t",
					lifted, testCase.wantLifted)
			}
		})
	}
}

// TestProperty17TheComparisonRejectsTheFlatAttrListBug checks that the property
// can actually fail.
//
// Both halves of Property 17 compare against something, and a comparison that
// accepts everything is the one defect randomized input cannot expose. So the two
// specific bugs this property exists to catch are simulated here and the
// comparisons are required to reject them: a handler that stored a flat attr list
// and applied its Handle-time groups to all of it, and a handler whose siblings
// share a backing array.
func TestProperty17TheComparisonRejectsTheFlatAttrListBug(t *testing.T) {
	ops := []handlerOp{
		p17AttrsOp(slog.String("a", "1")),
		p17GroupOp("g"),
		p17AttrsOp(slog.String("b", "2")),
	}
	batches := p17Batches(nil, ops, nil)
	correct := p17Expect(batches, "")

	if want := (map[string]any{"a": "1", "g": map[string]any{"b": "2"}}); !reflect.DeepEqual(correct.context, want) {
		t.Fatalf("the reference nester produced %s, want %s",
			pbtInputJSON(correct.context), pbtInputJSON(want))
	}

	// The bug: every batch bound to the group path open at Handle time, which is
	// what a flat attr list plus h.groups produces.
	handleTime := batches[len(batches)-1].groups
	flattened := make(p17BatchList, 0, len(batches))
	for _, batch := range batches {
		flattened = append(flattened, p17Batch{groups: handleTime, attrs: batch.attrs})
	}
	buggy := p17Expect(flattened, "")
	if reflect.DeepEqual(buggy.context, correct.context) {
		t.Errorf("binding every batch to the Handle-time group path produced the same tree as the "+
			"correct binding (%s); the comparison in this file cannot distinguish the bug it exists "+
			"to catch", pbtInputJSON(buggy.context))
	}

	// The leak detector must find a sibling's key wherever it is nested, and
	// must not invent one when the context is clean.
	leaked := map[string]any{
		"p17left_a0": "mine",
		"p17left_g0": map[string]any{"p17right_a0": "not mine", "nested": []any{"x"}},
	}
	if got := p17KeysWithPrefix(leaked, "p17right"); !equalStrings(got, []string{"p17right_a0"}) {
		t.Errorf("p17KeysWithPrefix found %v in a context holding a nested sibling key, want [p17right_a0]", got)
	}
	if got := p17KeysWithPrefix(leaked, "p17left"); len(got) != 2 {
		t.Errorf("p17KeysWithPrefix found %v for the owning branch, want both of its keys", got)
	}
	clean := map[string]any{"p17left_a0": "mine", "p17left_g0": map[string]any{"k": "v"}}
	if got := p17KeysWithPrefix(clean, "p17right"); len(got) != 0 {
		t.Errorf("p17KeysWithPrefix found %v in a clean context, want none", got)
	}
}

// p17AttrsOp and p17GroupOp are the two derivation operations spelled as data,
// so the tables above read as sequences rather than as struct literals.
func p17AttrsOp(as ...slog.Attr) handlerOp {
	return handlerOp{Kind: handlerOpAttrs, Attrs: as}
}

func p17GroupOp(name string) handlerOp {
	return handlerOp{Kind: handlerOpGroup, Group: name}
}
