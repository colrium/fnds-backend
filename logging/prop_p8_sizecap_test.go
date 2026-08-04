package logging

// Property 8 lives here, alone, so that the property this file asserts is
// obvious from its name and so that concurrently developed property tests
// cannot collide in one file. Example-based coverage of the same code lives in
// truncate_test.go; those cases pin specific behaviours (largest-first order,
// the last-resort stages, rune-boundary cutting) that a universal statement can
// satisfy vacuously, while the property below is the universal statement.
//
// Everything here drives capEventSize directly on a hand-built Log_Event. That
// is deliberate and matches Req 12.9: the size cap is a pure function of an
// Event, so no Log_Shipper, no goroutine, no clock, and no network is needed to
// state or check its properties.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// Feature: betterstack-error-logging, Property 8: Size cap is respected and privileged fields survive it
// **Validates: Requirements 3.12, 3.14, 3.17**
//
// For any Log_Event — including events carrying multi-megabyte messages and
// multi-megabyte attribute values — after capEventSize:
//
//   - the serialized event occupies at most maxEventBytes (Req 3.12), and the
//     cached serializedLen reports that exact number;
//   - `truncated` is true exactly when the event exceeded the cap before the
//     call or already carried the latch, and false otherwise (Req 3.14);
//   - dt, level, service, environment, error.type, and every source field are
//     byte-identical to their pre-cap values (Req 3.17);
//   - the payload is still valid UTF-8 JSON, and every string the cap shortened
//     ends with the truncation indicator.
//
// The loop reaching its end is itself part of the property: capEventSize must
// terminate on every input, including one whose bulk sits somewhere Req 3.12's
// two named stages cannot reach.
func TestProperty8_SizeCapIsRespectedAndPrivilegedFieldsSurvive(t *testing.T) {
	r, seed := pbtRand(t)

	// Non-vacuity counters. Almost every clause of this property is trivially
	// satisfiable by an event that already fits the cap, so a generator change
	// that stopped producing oversized events — or stopped producing events
	// arriving with the latch already set — would leave the test green while
	// checking nothing. The floors below are far under the observed counts
	// (roughly 30 oversized and 15 pre-latched per 100 draws), so they fail only
	// on a real loss of coverage, not on an unlucky seed.
	oversized, preLatched := 0, 0

	for iteration := 0; iteration < pbtIterations; iteration++ {
		ev := p8DrawEvent(r)

		before, ok := p8Observe(t, seed, iteration, ev)
		if !ok {
			continue
		}
		if before.size > maxEventBytes {
			oversized++
		}
		if before.truncated {
			preLatched++
		}

		capEventSize(ev)

		p8Verify(t, seed, iteration, before, ev)
	}

	const minOversized, minPreLatched = 5, 3
	if oversized < minOversized {
		t.Errorf("only %d of %d generated events exceeded the %d-byte cap, want at least %d: the property is close to vacuous",
			oversized, pbtIterations, maxEventBytes, minOversized)
	}
	if preLatched < minPreLatched {
		t.Errorf("only %d of %d generated events arrived with truncated already true, want at least %d: the latch clause is close to vacuous",
			preLatched, pbtIterations, minPreLatched)
	}
}

// Feature: betterstack-error-logging, Property 8: Size cap is respected and privileged fields survive it
// **Validates: Requirements 3.12, 3.14, 3.17**
//
// The same property, driven at the byte lengths that decide the off-by-one:
// exactly one byte under the cap, exactly at the cap, and exactly one byte over
// it. genSizedEvent hits each length exactly, so these are not "near the
// boundary" inputs — they are the boundary.
//
// A cap written with `<` instead of `<=`, or one measuring the message rather
// than the serialized event, passes the randomized loop above and fails here.
//
// One byte over is a subtler input than it looks. Latching `truncated` rewrites
// `"truncated":false` as `"truncated":true`, which is itself one byte shorter,
// so a 65537-byte event lands exactly on the cap from the latch alone and no
// string is cut. The fourth target is 64 bytes over the cap, far enough past it
// that the latch cannot account for the reduction and a string cut carrying the
// truncation indicator is unavoidable.
func TestProperty8_SizeCapHoldsAtTheExactBoundaryLengths(t *testing.T) {
	r, seed := pbtRand(t)

	// Far enough over the cap that no bookkeeping field flip can absorb the
	// overage, so stage 1 must cut the filler.
	const overageForcingACut = 64

	// Redrawn per target so the boundary is exercised with several different
	// timestamps and messages rather than one fixed payload.
	const drawsPerTarget = 3

	iteration := 0
	targets := []int{
		maxEventBytes - 1,
		maxEventBytes,
		maxEventBytes + 1,
		maxEventBytes + overageForcingACut,
	}
	for _, target := range targets {
		for draw := 0; draw < drawsPerTarget; draw++ {
			iteration++

			ev := genSizedEvent(r, target)

			before, ok := p8Observe(t, seed, iteration, ev)
			if !ok {
				continue
			}
			if before.size != target {
				// The generator's exactness is a harness invariant this test
				// rests on: without it, "one byte over the cap" is a guess.
				pbtFatalf(t, seed, iteration, ev,
					"genSizedEvent(%d) produced a %d-byte event; the boundary cannot be tested without exact sizing",
					target, before.size)
			}

			capEventSize(ev)

			p8Verify(t, seed, iteration, before, ev)

			if target <= maxEventBytes {
				// At and below the cap nothing may be touched at all: no
				// truncation, no latch, and the cached length is the length the
				// event already had (Req 3.14).
				if ev.Truncated {
					pbtFailf(t, seed, iteration, ev,
						"truncated = true for an event of exactly %d bytes, which is within the %d-byte cap (Req 3.14)",
						target, maxEventBytes)
				}
				if ev.SerializedLen() != target {
					pbtFailf(t, seed, iteration, ev,
						"cached serialized length = %d for an untouched %d-byte event, want %d",
						ev.SerializedLen(), target, target)
				}
				continue
			}

			// Over the cap: it must fire, and it must say so. p8Verify has
			// already checked that the result fits and that any string it
			// rewrote is a legitimate, indicator-bearing cut.
			if !ev.Truncated {
				pbtFailf(t, seed, iteration, ev,
					"truncated = false for an event of exactly %d bytes, which exceeds the %d-byte cap (Req 3.12)",
					target, maxEventBytes)
			}
			if target < maxEventBytes+overageForcingACut {
				continue
			}

			// At this overage the reduction cannot come from anywhere but the
			// filler, so the cut and its indicator are mandatory here.
			filler, isString := ev.Context[pbtSizeFillerKey].(string)
			if !isString {
				pbtFailf(t, seed, iteration, ev,
					"context[%q] is %T after capping a %d-byte event, want the truncated filler string",
					pbtSizeFillerKey, ev.Context[pbtSizeFillerKey], target)
				continue
			}
			if !strings.HasSuffix(filler, truncationIndicator) {
				pbtFailf(t, seed, iteration, ev,
					"the filler was not cut although the event was %d bytes over the cap (Req 3.12): ends %q",
					target-maxEventBytes, tail(filler))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Input drawing
// ---------------------------------------------------------------------------

// p8DrawEvent returns one generated input for Property 8.
//
// genEvent alone is not enough. Its default hazard set already reaches the
// serialized boundaries some of the time, but the cap's interesting behaviour is
// concentrated in inputs that are far over it, and in inputs whose bulk is not a
// context string and not the message — the case Req 3.12's two named stages
// cannot reach and the last-resort stages must. Those are drawn explicitly here
// rather than left to chance.
func p8DrawEvent(r *rand.Rand) *Event {
	switch {
	case pbtChance(r, 20):
		// Exactly-sized events, biased to 65535/65536/65537 by genSize, with
		// the occasional 1 MiB draw.
		return genSizedEvent(r, genSize(r))

	case pbtChance(r, 15):
		// Multi-megabyte message, no context at all: stage 1 finds nothing and
		// stage 2 has to carry the whole reduction.
		ev := genEvent(r)
		ev.Message = pbtFiller(2 << 20)
		ev.Context = nil
		return ev

	case pbtChance(r, 15):
		// Bulk that is not a string: no context string is long enough to cut,
		// so the cap is reachable only by dropping whole context entries. A
		// naive implementation loops forever here, which the test surfaces as a
		// timeout rather than a failure — the price of asserting termination.
		numbers := make([]any, 40000)
		for i := range numbers {
			numbers[i] = float64(i)
		}
		ev := genEvent(r)
		if ev.Context == nil {
			ev.Context = make(map[string]any, 1)
		}
		ev.Context["samples"] = numbers
		return ev

	default:
		return genEvent(r)
	}
}

// ---------------------------------------------------------------------------
// Observation and verification
// ---------------------------------------------------------------------------

// p8Snapshot is everything about an Event that has to be read *before*
// capEventSize runs, because capEventSize mutates the event in place.
//
// The privileged fields are copied by value; every truncatable string is copied
// into a path-keyed map so that a value changed by the cap can be identified and
// checked, and a value the cap had no business touching can be shown untouched.
type p8Snapshot struct {
	dt          string
	level       string
	service     string
	environment string

	hasError  bool
	errorType string

	hasSource bool
	source    SourceInfo

	// truncated is the latch state on arrival. It may already be true: the
	// error-chain cap (Req 3.7) and the context depth cap (Req 3.9) set it
	// during conversion, and the generator sets it on its own. The expected
	// post-cap value is derived from it, never assumed false.
	truncated bool

	// size is the serialized length before capping. It is the sole oracle for
	// whether truncation was required at all (Req 3.12, 3.14).
	size int

	// strings maps a path to the string value at that path, for every string
	// the cap is allowed to shorten.
	strings map[string]string
}

// p8Observe captures the pre-cap snapshot. It reports false when the generated
// event cannot be marshalled at all, in which case no statement about its
// serialized size is meaningful and the iteration is skipped.
func p8Observe(t *testing.T, seed int64, iteration int, ev *Event) (p8Snapshot, bool) {
	t.Helper()

	encoded, err := json.Marshal(ev)
	if err != nil {
		// Not expected: every generator this test draws from is marshalable by
		// construction (genEvent defaults to hazardsSerializable). Fatal rather
		// than skipped, because a silently skipped iteration would let the
		// property pass on inputs it never checked.
		pbtFatalf(t, seed, iteration, ev, "generated event does not marshal: %v", err)
		return p8Snapshot{}, false
	}

	snapshot := p8Snapshot{
		dt:          ev.DT,
		level:       ev.Level,
		service:     ev.Service,
		environment: ev.Environment,
		truncated:   ev.Truncated,
		size:        len(encoded),
		strings:     p8CollectStrings(ev),
	}
	if ev.Error != nil {
		snapshot.hasError = true
		snapshot.errorType = ev.Error.Type
	}
	if ev.Source != nil {
		snapshot.hasSource = true
		snapshot.source = *ev.Source
	}
	return snapshot, true
}

// p8Verify checks every clause of Property 8 against one capped event.
//
// It uses pbtFailf rather than pbtFatalf throughout, so a single run reports
// every clause an implementation violates instead of stopping at the first.
func p8Verify(t *testing.T, seed int64, iteration int, before p8Snapshot, ev *Event) {
	t.Helper()

	encoded, err := json.Marshal(ev)
	if err != nil {
		// The event marshalled during p8Observe and the cap only shortens
		// strings and deletes map entries, so a failure here means the cap
		// corrupted the event. Nothing after this point is checkable.
		pbtFatalf(t, seed, iteration, ev,
			"capped event no longer marshals (it did before capping, at %d bytes): %v", before.size, err)
		return
	}
	size := len(encoded)

	// Req 3.12: the cap is on the serialized event, and it is inclusive.
	if size > maxEventBytes {
		pbtFailf(t, seed, iteration, ev,
			"serialized event is %d bytes after capping, want at most %d (was %d bytes before) (Req 3.12)",
			size, maxEventBytes, before.size)
	}

	// The cached length is what the delivery goroutine's 1 MiB body check reads
	// (Req 5.14), so an over-reported or stale value is as harmful as an
	// oversized event.
	if got := ev.SerializedLen(); got != size {
		pbtFailf(t, seed, iteration, ev,
			"cached serialized length = %d, want the actual marshalled length %d", got, size)
	}
	if got := ev.SerializedLen(); got > maxEventBytes {
		pbtFailf(t, seed, iteration, ev,
			"cached serialized length = %d, want at most %d (Req 3.12)", got, maxEventBytes)
	}

	// Req 3.14: `truncated` is true exactly when the event needed truncation,
	// and the latch is never cleared. Anything the generator, the error-chain
	// cap, or the depth cap already set stays set.
	wantTruncated := before.truncated || before.size > maxEventBytes
	if ev.Truncated != wantTruncated {
		pbtFailf(t, seed, iteration, ev,
			"truncated = %t, want %t (pre-cap size %d, cap %d, latch on arrival %t) (Req 3.12, 3.14)",
			ev.Truncated, wantTruncated, before.size, maxEventBytes, before.truncated)
	}

	p8VerifyPrivilegedFields(t, seed, iteration, before, ev)

	// Req 3.13 still holds after cutting: the payload is valid UTF-8 JSON. A cut
	// landing inside a multi-byte rune is the way this breaks.
	if !utf8.Valid(encoded) {
		pbtFailf(t, seed, iteration, ev, "capped event is not valid UTF-8 (Req 3.13)")
	}
	if !json.Valid(encoded) {
		pbtFailf(t, seed, iteration, ev, "capped event is not valid JSON (Req 3.13)")
	}

	p8VerifyStringChanges(t, seed, iteration, before, ev)
}

// p8VerifyPrivilegedFields asserts Req 3.17: dt, level, service, environment,
// error.type, and every source field are byte-identical to their pre-cap values.
//
// The oracle is the untruncated event itself, as the design's P8 row states:
// these fields are bounded by construction — dt is 24 bytes, service and
// environment are capped at 64 characters by conversion, error.type and the
// source fields are runtime-produced identifiers — so the cap is always
// reachable without them and there is never an excuse to touch one.
func p8VerifyPrivilegedFields(t *testing.T, seed int64, iteration int, before p8Snapshot, ev *Event) {
	t.Helper()

	if ev.DT != before.dt {
		pbtFailf(t, seed, iteration, ev, "dt = %q, want %q (Req 3.17)", ev.DT, before.dt)
	}
	if ev.Level != before.level {
		pbtFailf(t, seed, iteration, ev, "level = %q, want %q (Req 3.17)", ev.Level, before.level)
	}
	if ev.Service != before.service {
		pbtFailf(t, seed, iteration, ev,
			"service = %q, want %q (Req 3.17)", pbtSafeString(ev.Service), pbtSafeString(before.service))
	}
	if ev.Environment != before.environment {
		pbtFailf(t, seed, iteration, ev,
			"environment = %q, want %q (Req 3.17)", ev.Environment, before.environment)
	}

	// error.type survives only if the error object survives at all, so its
	// disappearance is a Req 3.17 violation in its own right.
	switch {
	case before.hasError && ev.Error == nil:
		pbtFailf(t, seed, iteration, ev,
			"error object was removed, taking error.type %q with it (Req 3.17)", before.errorType)
	case before.hasError && ev.Error.Type != before.errorType:
		pbtFailf(t, seed, iteration, ev,
			"error.type = %q, want %q (Req 3.17)", ev.Error.Type, before.errorType)
	case !before.hasError && ev.Error != nil:
		pbtFailf(t, seed, iteration, ev, "error object appeared during capping: %+v", *ev.Error)
	}

	switch {
	case before.hasSource && ev.Source == nil:
		pbtFailf(t, seed, iteration, ev,
			"source object was removed, want %+v preserved (Req 3.17)", before.source)
	case before.hasSource && *ev.Source != before.source:
		pbtFailf(t, seed, iteration, ev,
			"source = %+v, want %+v (Req 3.17)", *ev.Source, before.source)
	case !before.hasSource && ev.Source != nil:
		pbtFailf(t, seed, iteration, ev, "source object appeared during capping: %+v", *ev.Source)
	}
}

// p8VerifyStringChanges asserts that every string difference the cap introduced
// is a legitimate truncation, and that an event which already fitted the cap was
// not modified at all.
//
// "Legitimate" means three things: the value got strictly shorter, it carries
// the truncation indicator so a reader can tell a cut value from a genuinely
// short one (Req 3.12), and it is still valid UTF-8 whenever the original was.
// The UTF-8 clause is conditional on purpose: cutString promises a rune-boundary
// cut for valid input, and the generator deliberately produces values that were
// already invalid, whose interior bytes no cut can repair. Those are normalized
// by conversion on the real Handle path, and by encoding/json on the wire, which
// is why the whole-payload utf8.Valid check above still holds unconditionally.
func p8VerifyStringChanges(t *testing.T, seed int64, iteration int, before p8Snapshot, ev *Event) {
	t.Helper()

	after := p8CollectStrings(ev)
	fitted := before.size <= maxEventBytes

	for path, got := range after {
		want, existed := before.strings[path]
		switch {
		case !existed:
			pbtFailf(t, seed, iteration, ev,
				"a string appeared at %s during capping: %s", path, pbtSafeString(got))

		case got == want:
			// Untouched, which is the common case even for a capped event: the
			// longest slot normally absorbs the whole overage on its own.

		case fitted:
			pbtFailf(t, seed, iteration, ev,
				"value at %s was rewritten although the event already fitted the cap at %d bytes (Req 3.14): %s -> %s",
				path, before.size, pbtSafeString(want), pbtSafeString(got))

		default:
			if len(got) >= len(want) {
				pbtFailf(t, seed, iteration, ev,
					"value at %s did not shrink: %d bytes -> %d bytes", path, len(want), len(got))
			}
			if !strings.HasSuffix(got, truncationIndicator) {
				pbtFailf(t, seed, iteration, ev,
					"truncated value at %s does not end with the truncation indicator (Req 3.12): ends %q",
					path, tail(got))
			}
			if utf8.ValidString(want) && !utf8.ValidString(got) {
				pbtFailf(t, seed, iteration, ev,
					"truncated value at %s is not valid UTF-8 although the original was (Req 3.13)", path)
			}
		}
	}

	if fitted && len(after) != len(before.strings) {
		pbtFailf(t, seed, iteration, ev,
			"string count changed from %d to %d for an event that already fitted the cap at %d bytes (Req 3.14)",
			len(before.strings), len(after), before.size)
	}
}

// ---------------------------------------------------------------------------
// String collection
// ---------------------------------------------------------------------------

// p8MaxWalkDepth bounds the collection walk. It exceeds every generated nesting
// budget and both production caps, so nothing a generator can build is missed,
// while the walk still terminates on a hand-built event of arbitrary depth.
const p8MaxWalkDepth = 20

// p8CollectStrings returns every string in ev that the size cap is permitted to
// shorten, keyed by an unambiguous path.
//
// This is an independent walker, not a call into collectContextStrings: reusing
// the implementation's own traversal would make the property blind to a bug in
// that traversal, which is exactly the class of bug — a string the cap forgets
// to reach, or one it reaches that it should not — this property exists to
// catch.
//
// Privileged fields are absent by design (Req 3.17). They are compared for
// byte-identity in p8VerifyPrivilegedFields instead, so their presence here
// would let a truncated privileged field pass as a legitimate cut.
func p8CollectStrings(ev *Event) map[string]string {
	collected := make(map[string]string, 8)
	if ev == nil {
		return collected
	}

	collected["message"] = ev.Message
	if ev.Error != nil {
		// error.message and each chain entry are cuttable; error.type is not.
		collected["error.message"] = ev.Error.Message
		for i, link := range ev.Error.Chain {
			collected[fmt.Sprintf("error.chain[%d]", i)] = link
		}
	}
	if ev.Request != nil {
		collected["request.id"] = ev.Request.ID
		collected["request.method"] = ev.Request.Method
		collected["request.path"] = ev.Request.Path
	}
	if ev.User != nil {
		collected["user.id"] = ev.User.ID
	}
	p8CollectValueStrings(ev.Context, "context", 0, collected)

	return collected
}

// p8CollectValueStrings appends every string reachable under value, which sits
// at the given depth, to collected.
//
// Paths are built with quoted map keys and bracketed slice indices — for
// example `context["outer"]["inner"][0]` — so that a generated key containing a
// dot, a bracket, or an empty string cannot collide with the path of a different
// value and mask a change.
func p8CollectValueStrings(value any, path string, depth int, collected map[string]string) {
	if depth > p8MaxWalkDepth {
		return
	}

	switch typed := value.(type) {
	case string:
		collected[path] = typed
	case map[string]any:
		for key, child := range typed {
			p8CollectValueStrings(child, fmt.Sprintf("%s[%q]", path, key), depth+1, collected)
		}
	case []any:
		for i, child := range typed {
			p8CollectValueStrings(child, fmt.Sprintf("%s[%d]", path, i), depth+1, collected)
		}
	}
}
