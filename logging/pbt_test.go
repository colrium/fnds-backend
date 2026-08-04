package logging

// Property-test harness. This file contains **no property tests** — only the
// seeding, generators, and failure reporting that every property test in this
// package builds on (Req 12.6). The properties themselves live beside the code
// they exercise: conversion properties in event_test.go, redaction properties in
// redact_test.go and redact_depth_test.go, and so on.
//
// Why hand-rolled instead of a library: Req 12.6 bars new go.mod requirements,
// and Go has no test-only dependency scope, so a property-testing library would
// land in go.mod even though no production code imports it. testing/quick is
// unsuitable — it has no shrinking, and generating slog.Record values with
// nested groups and hostile attribute types through quick.Generator is more code
// than these generators. So: randomized table-driven loops over math/rand,
// seeded deterministically.
//
// The accepted cost is **no automatic shrinking**. A counterexample arrives at
// full generated size and has to be minimized by hand, so two things
// compensate:
//
//  1. Generators are biased toward boundaries — empty strings, group depths
//     8/9/10/11, serialized sizes 65535/65536/65537, HTTP statuses
//     199/200/202/299/402/403/406/413/429/499/500/599/600 — which makes most
//     counterexamples small by construction.
//  2. pbtFailf reports the offending input as indented JSON together with the
//     seed and iteration index, so a failure can be replayed with
//     FUNDIS_PBT_SEED and pasted into an example-based regression table.
//
// API stability matters here: every later property test depends on these
// signatures. The shape below matches the design document's harness sketch
// exactly — pbtIterations, pbtSeed, genRecord, genAttr, genEvent,
// genHandlerOps, genStatus — and each of those has a *With sibling taking an
// explicit hazard set, because some properties want hostile input (P3, P8) and
// others want clean, JSON-representable input (P2, P4, P5).
//
// Every generator takes an explicit *rand.Rand. No generator reads a package
// variable, the clock, or the environment, so a run is fully reproducible from
// the logged seed.

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pbtIterations is the minimum number of generated inputs per property, per the
// testing standard. A property may run more; none runs fewer.
const pbtIterations = 100

const (
	// pbtSeedEnv overrides the seed for exploratory runs:
	//
	//	FUNDIS_PBT_SEED=12345 go test ./logging/... -run Property -count=1
	pbtSeedEnv = "FUNDIS_PBT_SEED"

	// pbtDefaultSeed keeps CI deterministic. A fixed default means a green CI
	// run proves the same 100 inputs pass every time, and a red one is
	// reproducible without reading the log.
	pbtDefaultSeed int64 = 0x5EED
)

// pbtSeed returns the seed for this test, honouring FUNDIS_PBT_SEED, and always
// logs the value used so a failure is replayable from the test output alone.
func pbtSeed(t *testing.T) int64 {
	t.Helper()
	return pbtSeedFrom(t, os.Getenv(pbtSeedEnv))
}

// pbtSeedFrom is the pure core of pbtSeed: it takes the raw override value
// rather than reading the environment, so the harness's own self-test can
// exercise both branches without mutating process state.
//
// An unparseable override fails the test rather than falling back to the
// default, because silently ignoring it would make an exploratory run look like
// it had covered a seed it never used.
func pbtSeedFrom(t *testing.T, raw string) int64 {
	t.Helper()

	seed := pbtDefaultSeed
	if raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("invalid %s %q: %v", pbtSeedEnv, raw, err)
		}
		seed = parsed
	}
	t.Logf("pbt seed = %d", seed)
	return seed
}

// pbtRand returns a generator seeded from pbtSeed together with that seed, which
// is the pair every property test needs: the *rand.Rand to draw inputs from and
// the seed to quote in a counterexample.
//
//	r, seed := pbtRand(t)
//	for i := 0; i < pbtIterations; i++ {
//	    rec := genRecord(r)
//	    if !holds(rec) {
//	        pbtFailf(t, seed, i, rec, "property violated")
//	    }
//	}
func pbtRand(t *testing.T) (*rand.Rand, int64) {
	t.Helper()

	seed := pbtSeed(t)
	return rand.New(rand.NewSource(seed)), seed
}

// ---------------------------------------------------------------------------
// Hazard selection
// ---------------------------------------------------------------------------

// hazards selects which classes of hostile input a generator may produce.
//
// It exists because the properties disagree about what they want. P3 (hostile
// input never panics the caller) needs channels, cycles, and panicking methods;
// P2 (conversion round-trips through JSON) needs inputs whose expected output a
// reference model can state, which rules those out. One generator with an
// explicit hazard set beats two divergent generator families.
type hazards uint16

const (
	// hazardNil permits nil and typed-nil attribute values (Req 3.13).
	hazardNil hazards = 1 << iota
	// hazardInvalidUTF8 permits invalid UTF-8 byte sequences in keys and in
	// string values (Req 3.13).
	hazardInvalidUTF8
	// hazardEmptyKeys permits empty attribute keys, empty group names, and the
	// zero Attr, all of which the slog.Handler contract gives special meaning.
	hazardEmptyKeys
	// hazardSensitiveKeys permits keys drawn from sensitiveSubstrings, so
	// redaction properties see keys that actually match (Req 10.1).
	hazardSensitiveKeys
	// hazardLargeStrings permits strings sized at the serialization boundaries,
	// so size-cap properties see events on either side of 65536 bytes.
	hazardLargeStrings
	// hazardUnrepresentable permits values encoding/json cannot represent:
	// channels, funcs, complex numbers, and structs containing them.
	hazardUnrepresentable
	// hazardCyclic permits self-referential maps and slices. Values generated
	// under this hazard are not json.Marshal-able, so a property that marshals
	// its generated input directly must leave it off.
	hazardCyclic
	// hazardPanicStringer permits a value whose String method panics, reached
	// through fmt because encoding/json cannot represent it.
	hazardPanicStringer
	// hazardPanicLogValuer permits a value whose LogValue method panics.
	hazardPanicLogValuer
)

const (
	// hazardNone generates clean, JSON-representable, well-keyed input: no nils,
	// no invalid UTF-8, no empty keys, no oversized strings. This is what a
	// property with a reference model over expected key paths wants.
	hazardNone hazards = 0

	// hazardsSerializable is hostile but still marshalable: nils, invalid UTF-8,
	// empty keys, sensitive keys, and boundary-sized strings, with no cycles, no
	// unrepresentable types, and no panicking methods. It is the default for
	// genEvent, because an Event is a value later stages marshal directly.
	hazardsSerializable = hazardNil | hazardInvalidUTF8 | hazardEmptyKeys |
		hazardSensitiveKeys | hazardLargeStrings

	// hazardsAll enables every hazard. It is the default for genRecord and
	// genAttr, because a Log_Record arrives from an arbitrary call site and the
	// handler must survive all of it (Req 8.5, 8.8).
	hazardsAll = hazardsSerializable | hazardUnrepresentable | hazardCyclic |
		hazardPanicStringer | hazardPanicLogValuer
)

// has reports whether h enables every hazard in want.
func (h hazards) has(want hazards) bool { return h&want == want }

// ---------------------------------------------------------------------------
// Small drawing helpers
// ---------------------------------------------------------------------------

// pbtChance reports true with the given percentage probability.
func pbtChance(r *rand.Rand, percent int) bool { return r.Intn(100) < percent }

// pbtPick returns a uniformly chosen element of options, which must be
// non-empty.
func pbtPick[T any](r *rand.Rand, options []T) T { return options[r.Intn(len(options))] }

// pbtWords are ordinary short values, so a generated payload reads like a real
// log line and a counterexample is legible.
var pbtWords = []string{
	"", "ok", "queued", "job-42", "retry", "fundis", "202", "a b c",
	"héllo", "日本語", "line\nbreak", "quote\"inside", "\t",
}

// pbtBenignKeys are attribute keys that match no sensitive substring, so a
// generated key is only sensitive when the generator deliberately made it so.
var pbtBenignKeys = []string{
	"job_id", "user_id", "count", "detail", "attempt", "path", "reason",
	"status", "duration", "nested", "a", "b",
}

// pbtInvalidUTF8Strings carry byte sequences that are not valid UTF-8, which
// conversion must replace rather than pass through (Req 3.13).
var pbtInvalidUTF8Strings = []string{
	"\xff", "value \xff\xfe here", "\x80start", "end\xc3", "mixed \xed\xa0\x80 surrogate",
}

// ---------------------------------------------------------------------------
// Boundary-biased scalar generators
// ---------------------------------------------------------------------------

// pbtDepthBoundaries straddle both nesting caps: maxContextDepth is 8, so 8 is
// the deepest retained ctxLevel and 9 the first omitted one, and maxRedactDepth
// is 10, so rootDepth 11 is the first truncated level.
var pbtDepthBoundaries = []int{8, 9, 10, 11}

// genDepth returns a group-nesting budget biased toward the cap boundaries.
func genDepth(r *rand.Rand) int {
	if pbtChance(r, 60) {
		return pbtPick(r, pbtDepthBoundaries)
	}
	return r.Intn(13)
}

// pbtSizeBoundaries straddle the 64 KiB serialized-event cap (Req 3.12) and the
// empty case.
var pbtSizeBoundaries = []int{0, 1, 65535, 65536, 65537}

// genSize returns a byte size biased toward the size-cap boundaries, with an
// occasional multi-megabyte draw so the cap is exercised far above the bound as
// well as one byte either side of it.
func genSize(r *rand.Rand) int {
	if pbtChance(r, 60) {
		return pbtPick(r, pbtSizeBoundaries)
	}
	if pbtChance(r, 5) {
		return 1 << 20
	}
	return r.Intn(70000)
}

// pbtStatusBoundaries are every branch boundary of the Req 7 status
// classification table: the edges of 2xx (199/200/299), the success code (202),
// the terminal non-retryable codes (402/403/406/413), the rate-limit code (429),
// the edges of 5xx (499/500/599), and one code above it (600).
//
// 199 and 600 are classification inputs, not transmittable responses: net/http
// treats a 1xx as informational, so a test that needs those two should feed the
// pure classifier rather than an httptest.Server status.
var pbtStatusBoundaries = []int{199, 200, 202, 299, 402, 403, 406, 413, 429, 499, 500, 599, 600}

// genStatus returns an HTTP status code biased toward the classification
// boundaries, so P11 reaches every branch of the status table rather than
// sampling 2xx and 5xx interiors 100 times.
func genStatus(r *rand.Rand) int {
	if pbtChance(r, 70) {
		return pbtPick(r, pbtStatusBoundaries)
	}
	return 200 + r.Intn(400) // 200..599, all servable by httptest
}

// genLevel returns a slog level, including the offset levels Req 2.7 allows and
// levels on both sides of every named threshold.
func genLevel(r *rand.Rand) slog.Level {
	if pbtChance(r, 80) {
		return pbtPick(r, []slog.Level{
			slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError,
			slog.LevelError + 2, slog.LevelWarn + 1, slog.LevelDebug - 4,
		})
	}
	return slog.Level(r.Intn(33) - 8)
}

// pbtTimeBase anchors generated timestamps so a counterexample's dt is stable
// and readable rather than an arbitrary epoch offset.
var pbtTimeBase = time.Date(2025, time.June, 14, 9, 21, 44, 317_000_000, time.UTC)

// genTime returns a timestamp, occasionally the zero time, which formats to
// year 1 and is the one value a naive dt implementation gets wrong.
func genTime(r *rand.Rand) time.Time {
	if pbtChance(r, 10) {
		return time.Time{}
	}
	return pbtTimeBase.Add(time.Duration(r.Int63n(int64(48*time.Hour))) - 24*time.Hour)
}

// genString returns a string value, biased toward the empty string and
// occasionally hostile according to h.
func genString(r *rand.Rand, h hazards) string {
	switch {
	case pbtChance(r, 15):
		return "" // the empty-string boundary, drawn often on purpose
	case h.has(hazardLargeStrings) && pbtChance(r, 5):
		return pbtFiller(genSize(r))
	case h.has(hazardInvalidUTF8) && pbtChance(r, 15):
		return pbtPick(r, pbtInvalidUTF8Strings)
	default:
		return pbtPick(r, pbtWords)
	}
}

// pbtFiller returns n bytes of ASCII. ASCII matters: it survives JSON encoding
// one byte per character, which is what lets genSizedEvent hit an exact
// serialized length.
func pbtFiller(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("x", n)
}

// genKey returns an attribute key. Sensitive keys are constructed so that
// matchesSensitiveKey is guaranteed to fire, rather than hoping a random word
// happens to contain a sensitive substring.
func genKey(r *rand.Rand, h hazards) string {
	switch {
	case h.has(hazardEmptyKeys) && pbtChance(r, 10):
		return ""
	case h.has(hazardSensitiveKeys) && pbtChance(r, 20):
		return genSensitiveKey(r)
	case h.has(hazardInvalidUTF8) && pbtChance(r, 10):
		return pbtPick(r, pbtBenignKeys) + "\xff"
	default:
		return pbtPick(r, pbtBenignKeys)
	}
}

// genSensitiveKey returns a key that matchesSensitiveKey accepts, spelled the
// way real call sites spell them: bare, prefixed, suffixed, or upper-cased, to
// exercise the case-insensitive substring rule (Req 10.1).
func genSensitiveKey(r *rand.Rand) string {
	substring := pbtPick(r, sensitiveSubstrings)
	switch r.Intn(4) {
	case 0:
		return substring
	case 1:
		return "user_" + substring
	case 2:
		return substring + "_value"
	default:
		return strings.ToUpper(substring)
	}
}

// ---------------------------------------------------------------------------
// Hostile value types
// ---------------------------------------------------------------------------

// pbtScalarStruct is representable by encoding/json, so it round-trips into a
// JSON object and the Redactor can walk inside it.
type pbtScalarStruct struct {
	A int    `json:"a"`
	B string `json:"b"`
}

// pbtChanStruct is not representable by encoding/json — the exported channel
// field is what makes json.Marshal fail — so conversion must fall back to Go
// formatting for it (Req 3.13).
type pbtChanStruct struct {
	Ch chan int `json:"ch"`
}

// pbtPanicStringer's String method panics, and it carries an exported channel
// so that it actually gets called: json.Marshal fails on the channel,
// conversion falls back to fmt, and fmt invokes String. A panicking method that
// is never reached would not test anything (Req 8.8).
type pbtPanicStringer struct {
	Ch chan int `json:"ch"`
}

func (pbtPanicStringer) String() string { panic("pbt: String panicked") }

// pbtPanicLogValuer's LogValue method panics, exercising the resolve step
// (Req 8.8, 10.8).
type pbtPanicLogValuer struct{}

func (pbtPanicLogValuer) LogValue() slog.Value { panic("pbt: LogValue panicked") }

// pbtLogValuer resolves normally, so the well-behaved LogValuer path is
// generated as often as the panicking one.
type pbtLogValuer struct{ value string }

func (v pbtLogValuer) LogValue() slog.Value { return slog.StringValue(v.value) }

// pbtError is a wrapping error, used to generate chains on both sides of the
// 10-entry error-chain cap (Req 3.7).
type pbtError struct {
	message string
	wrapped error
}

func (e *pbtError) Error() string {
	if e.wrapped == nil {
		return e.message
	}
	return e.message + ": " + e.wrapped.Error()
}

func (e *pbtError) Unwrap() error { return e.wrapped }

// pbtSelfError unwraps to itself. Nothing but the chain cap terminates a walk
// over it, which is exactly the point.
type pbtSelfError struct{}

func (e pbtSelfError) Error() string { return "pbt: self-unwrapping error" }

func (e pbtSelfError) Unwrap() error { return e }

// genError returns an error with a chain length spanning the cap, occasionally
// self-unwrapping, and occasionally a typed nil under hazardNil — a non-nil
// error interface holding a nil pointer, which must not be treated as an error.
func genError(r *rand.Rand, h hazards) error {
	if h.has(hazardNil) && pbtChance(r, 10) {
		return (*pbtError)(nil)
	}
	if pbtChance(r, 5) {
		return pbtSelfError{}
	}

	var err error = &pbtError{message: "root: " + pbtPick(r, pbtWords)}
	for links := r.Intn(13); links > 0; links-- {
		err = &pbtError{message: pbtPick(r, pbtBenignKeys), wrapped: err}
	}
	return err
}

// pbtCyclicMap returns a map that contains itself.
func pbtCyclicMap() map[string]any {
	m := map[string]any{"name": "cycle"}
	m["self"] = m
	return m
}

// pbtCyclicSlice returns a slice whose only element is itself.
func pbtCyclicSlice() []any {
	s := make([]any, 1)
	s[0] = s
	return s
}

// ---------------------------------------------------------------------------
// slog value and attribute generators
// ---------------------------------------------------------------------------

// genValue returns one attribute value. depth is the remaining group-nesting
// budget: at depth 0 the result is always a leaf, and a generated group's
// children are drawn at depth-1, so nesting never exceeds depth levels.
//
// Group probability times children per group is below 1, so the recursion is
// subcritical and a large depth budget does not produce an exponentially large
// value.
func genValue(r *rand.Rand, depth int, h hazards) slog.Value {
	if depth > 0 && pbtChance(r, 30) {
		return slog.GroupValue(genAttrsWith(r, depth-1, h, 1+r.Intn(3))...)
	}

	leaves := []func(*rand.Rand, hazards) slog.Value{
		func(r *rand.Rand, h hazards) slog.Value { return slog.StringValue(genString(r, h)) },
		func(r *rand.Rand, _ hazards) slog.Value {
			return slog.Int64Value(pbtPick(r, []int64{0, -1, 1, math.MaxInt64, math.MinInt64, r.Int63()}))
		},
		func(r *rand.Rand, _ hazards) slog.Value {
			return slog.Uint64Value(pbtPick(r, []uint64{0, 1, math.MaxUint64, uint64(r.Int63())}))
		},
		func(r *rand.Rand, _ hazards) slog.Value {
			return slog.Float64Value(pbtPick(r, []float64{
				0, -0, 1.5, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, r.NormFloat64(),
			}))
		},
		func(r *rand.Rand, _ hazards) slog.Value { return slog.BoolValue(pbtChance(r, 50)) },
		func(r *rand.Rand, _ hazards) slog.Value {
			return slog.DurationValue(time.Duration(r.Int63n(int64(time.Hour))) - 30*time.Minute)
		},
		func(r *rand.Rand, _ hazards) slog.Value { return slog.TimeValue(genTime(r)) },
		func(r *rand.Rand, h hazards) slog.Value { return slog.AnyValue(genError(r, h)) },
		func(r *rand.Rand, h hazards) slog.Value {
			return slog.AnyValue(pbtScalarStruct{A: r.Intn(100), B: genString(r, h)})
		},
		func(r *rand.Rand, h hazards) slog.Value {
			return slog.AnyValue(map[string]any{genKey(r, h): genString(r, h)})
		},
		func(r *rand.Rand, h hazards) slog.Value {
			return slog.AnyValue([]any{genString(r, h), r.Intn(10), pbtChance(r, 50)})
		},
		func(r *rand.Rand, h hazards) slog.Value { return slog.AnyValue(pbtLogValuer{value: genString(r, h)}) },
	}

	if h.has(hazardNil) {
		leaves = append(leaves,
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(nil) },
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue((*pbtScalarStruct)(nil)) },
		)
	}
	if h.has(hazardUnrepresentable) {
		leaves = append(leaves,
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(make(chan int)) },
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(func() {}) },
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(complex(1, 2)) },
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(pbtChanStruct{Ch: make(chan int)}) },
		)
	}
	if h.has(hazardCyclic) {
		leaves = append(leaves,
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(pbtCyclicMap()) },
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(pbtCyclicSlice()) },
		)
	}
	if h.has(hazardPanicStringer) {
		leaves = append(leaves,
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(pbtPanicStringer{Ch: make(chan int)}) },
		)
	}
	if h.has(hazardPanicLogValuer) {
		leaves = append(leaves,
			func(*rand.Rand, hazards) slog.Value { return slog.AnyValue(pbtPanicLogValuer{}) },
		)
	}

	return pbtPick(r, leaves)(r, h)
}

// genAttr returns one attribute with every hazard enabled: nested groups, the
// zero Attr, nils, invalid UTF-8 in keys and values, channel and func values,
// self-referential containers, and values whose String or LogValue method
// panics.
//
// depth is the remaining group-nesting budget; pass genDepth(r) to sample the
// cap boundaries.
func genAttr(r *rand.Rand, depth int) slog.Attr {
	return genAttrWith(r, depth, hazardsAll)
}

// genAttrWith is genAttr with an explicit hazard set. Use hazardNone for a
// clean attribute whose expected output a reference model can state.
func genAttrWith(r *rand.Rand, depth int, h hazards) slog.Attr {
	if h.has(hazardEmptyKeys) && pbtChance(r, 5) {
		// The zero Attr, which the slog.Handler contract requires be ignored.
		return slog.Attr{}
	}
	return slog.Attr{Key: genKey(r, h), Value: genValue(r, depth, h)}
}

// genAttrsWith returns n attributes drawn at the given depth budget.
func genAttrsWith(r *rand.Rand, depth int, h hazards, n int) []slog.Attr {
	attrs := make([]slog.Attr, 0, n)
	for i := 0; i < n; i++ {
		attrs = append(attrs, genAttrWith(r, depth, h))
	}
	return attrs
}

// genNestedGroupAttr returns a chain of exactly depth nested groups with one
// leaf attribute at the bottom, so the leaf sits at ctxLevel depth when the
// attr is added at the root of `context`.
//
// Unlike genAttr, the depth here is exact rather than a budget, which is what a
// depth-cap property needs: at depth 8 the leaf must survive conversion, at
// depth 9 it must be dropped and latch `truncated` (Req 3.9).
func genNestedGroupAttr(r *rand.Rand, depth int, h hazards) slog.Attr {
	leaf := slog.Attr{Key: genKey(r, h), Value: genValue(r, 0, h)}
	if depth <= 1 {
		return leaf
	}
	for level := depth; level > 1; level-- {
		leaf = slog.Attr{Key: pbtPick(r, pbtBenignKeys), Value: slog.GroupValue(leaf)}
	}
	return leaf
}

// genRecord returns a Log_Record with every hazard enabled: an arbitrary level
// including offsets, a message that may be empty or invalid UTF-8, a program
// counter that is sometimes absent, and hostile attributes at a
// boundary-sampled group depth.
func genRecord(r *rand.Rand) slog.Record {
	return genRecordWith(r, hazardsAll)
}

// genRecordWith is genRecord with an explicit hazard set.
func genRecordWith(r *rand.Rand, h hazards) slog.Record {
	record := slog.NewRecord(genTime(r), genLevel(r), genString(r, h), genPC(r))
	record.AddAttrs(genAttrsWith(r, genDepth(r), h, r.Intn(5))...)
	return record
}

// genPC returns a program counter, zero often enough that the "no call site,
// omit `source`" path is exercised (Req 3.16) and a real one otherwise so
// sourceFromPC resolves a genuine frame (Req 3.8).
func genPC(r *rand.Rand) uintptr {
	if pbtChance(r, 40) {
		return 0
	}
	var pcs [1]uintptr
	if runtime.Callers(2, pcs[:]) == 0 {
		return 0
	}
	return pcs[0]
}

// ---------------------------------------------------------------------------
// Event generator
// ---------------------------------------------------------------------------

// genEvent returns a hand-built Log_Event that json.Marshal can encode: hostile
// in keys, strings, and sizes, but free of cycles, unrepresentable types, and
// panicking methods, since an Event is a value later stages marshal directly.
//
// Use genEventWith(r, hazardsAll) for a cyclic or otherwise unmarshalable
// event, which is what the redaction depth cap needs (Req 10.10).
func genEvent(r *rand.Rand) *Event {
	return genEventWith(r, hazardsSerializable)
}

// genEventWith is genEvent with an explicit hazard set. Under
// hazardLargeStrings it sometimes returns a genSizedEvent, so a run sees events
// whose serialized length is exactly 65535, 65536, and 65537 bytes rather than
// merely near them.
func genEventWith(r *rand.Rand, h hazards) *Event {
	if h.has(hazardLargeStrings) && pbtChance(r, 15) {
		return genSizedEvent(r, genSize(r))
	}

	ev := &Event{
		DT:          formatTimestamp(genTime(r)),
		Level:       genLevel(r).String(),
		Message:     genString(r, h),
		Service:     fieldOrUnknown(genString(r, h)),
		Environment: pbtPick(r, []string{"production", "staging", "development", ""}),
		// Generated in both states so a property that must not clear the latch
		// sees events that already carry it.
		Truncated: pbtChance(r, 30),
	}

	if pbtChance(r, 60) {
		ev.Error = &ErrorInfo{
			Message: genString(r, h),
			Type:    pbtPick(r, []string{"*errors.errorString", "*fmt.wrapError", "*logging.pbtError"}),
		}
		for links := r.Intn(4); links > 0; links-- {
			ev.Error.Chain = append(ev.Error.Chain, genString(r, h))
		}
	}
	if pbtChance(r, 60) {
		ev.Source = &SourceInfo{
			File:     pbtPick(r, []string{"handler.go", "shipper.go", ""}),
			Line:     r.Intn(2000),
			Function: pbtPick(r, []string{"backend/logging.(*Handler).Handle", ""}),
		}
	}
	if pbtChance(r, 50) {
		ev.Request = &RequestInfo{
			ID:     genString(r, h),
			Method: pbtPick(r, []string{"GET", "POST", "PATCH", ""}),
			Path:   pbtPick(r, []string{"/api/v1/jobs/:jobId", "<unmatched>", ""}),
		}
	}
	if pbtChance(r, 40) {
		ev.User = &UserInfo{ID: genString(r, h)}
	}
	if pbtChance(r, 85) {
		ev.Context = genJSONObject(r, genDepth(r), h)
	}
	return ev
}

// genJSONObject returns a `context` object in the normalized shape conversion
// produces: scalars, map[string]any, and []any only.
//
// The shape is load-bearing for redaction properties. The Redactor descends
// into map[string]any and []any and treats every other container as a leaf, so
// generating a map[string]string here would silently hide keys from the walk
// and make a redaction property pass for the wrong reason.
func genJSONObject(r *rand.Rand, depth int, h hazards) map[string]any {
	object := make(map[string]any, 3)
	for keys := 1 + r.Intn(3); keys > 0; keys-- {
		object[genKey(r, h)] = genJSONValue(r, depth, h)
	}
	return object
}

// genJSONValue returns one normalized value at the given remaining depth
// budget.
func genJSONValue(r *rand.Rand, depth int, h hazards) any {
	if depth > 0 && pbtChance(r, 35) {
		if pbtChance(r, 30) {
			items := make([]any, 1+r.Intn(2))
			for i := range items {
				items[i] = genJSONValue(r, depth-1, h)
			}
			return items
		}
		return genJSONObject(r, depth-1, h)
	}

	leaves := []func() any{
		func() any { return genString(r, h) },
		func() any { return float64(r.Intn(1000)) },
		func() any { return json.Number(strconv.Itoa(r.Int())) },
		func() any { return pbtChance(r, 50) },
		func() any { return []any{} },
		func() any { return map[string]any{} },
	}
	if h.has(hazardNil) {
		leaves = append(leaves, func() any { return nil })
	}
	if h.has(hazardCyclic) {
		leaves = append(leaves,
			func() any { return pbtCyclicMap() },
			func() any { return pbtCyclicSlice() },
		)
	}
	return pbtPick(r, leaves)()
}

// pbtSizeFillerKey is the context key genSizedEvent pads with. It matches no
// sensitive substring, so the filler cannot be redacted out from under a
// size-cap assertion.
const pbtSizeFillerKey = "filler"

// genSizedEvent returns an Event whose JSON encoding is exactly target bytes
// long, or the shortest encoding it can produce when target is below that
// minimum.
//
// Exactness is achieved by measuring the event with an empty filler and padding
// with ASCII, which encoding/json emits one byte per character. That is what
// makes "one byte under, exactly at, one byte over the 64 KiB cap" a testable
// input rather than an approximation.
func genSizedEvent(r *rand.Rand, target int) *Event {
	ev := &Event{
		DT:          formatTimestamp(genTime(r)),
		Level:       slog.LevelError.String(),
		Message:     pbtPick(r, pbtWords),
		Service:     "fundis-backend",
		Environment: "production",
		Context:     map[string]any{pbtSizeFillerKey: ""},
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		// Unreachable: every field above is a plain string or map of strings.
		return ev
	}
	if pad := target - len(raw); pad > 0 {
		ev.Context[pbtSizeFillerKey] = pbtFiller(pad)
	}
	return ev
}

// ---------------------------------------------------------------------------
// Handler derivation operations
// ---------------------------------------------------------------------------

// handlerOpKind distinguishes the two derivation operations slog.Handler
// exposes.
type handlerOpKind int

const (
	// handlerOpAttrs is a WithAttrs call carrying Attrs.
	handlerOpAttrs handlerOpKind = iota
	// handlerOpGroup is a WithGroup call carrying Group.
	handlerOpGroup
)

// handlerOp is one WithAttrs or WithGroup call, so a derivation sequence can be
// generated, applied, and reported as data (Property 17).
//
// One type covers both operations rather than two, because the property is
// about their *interleaving*: WithAttrs(a).WithGroup("g").WithAttrs(b) must
// place a at context.a and b at context.g.b, and only a single ordered sequence
// can express that.
type handlerOp struct {
	Kind  handlerOpKind
	Group string
	Attrs []slog.Attr
}

// apply performs this operation on h and returns the derived handler, so a test
// folds a sequence with a plain loop:
//
//	derived := slog.Handler(root)
//	for _, op := range ops {
//	    derived = op.apply(derived)
//	}
func (op handlerOp) apply(h slog.Handler) slog.Handler {
	if op.Kind == handlerOpGroup {
		return h.WithGroup(op.Group)
	}
	return h.WithAttrs(op.Attrs)
}

// pbtGroupNames is a deliberately small pool: repeats make two frames open the
// same group name, which is the group-merge case (Req 3.11), and the empty name
// is the documented WithGroup no-op.
var pbtGroupNames = []string{"a", "b", "auth", "", "a"}

// genHandlerOps returns a WithAttrs/WithGroup sequence for Property 17,
// including the two contract edge cases: an empty attr batch and an empty group
// name, both of which must leave the handler unchanged.
func genHandlerOps(r *rand.Rand) []handlerOp {
	return genHandlerOpsWith(r, hazardsSerializable)
}

// genHandlerOpsWith is genHandlerOps with an explicit hazard set. The default
// excludes panicking and unrepresentable values, because a derivation property
// compares the emitted `context` object against a reference nester and needs
// attribute values whose rendering is well-defined.
func genHandlerOpsWith(r *rand.Rand, h hazards) []handlerOp {
	ops := make([]handlerOp, 0, 6)
	for count := r.Intn(7); count > 0; count-- {
		if pbtChance(r, 40) {
			ops = append(ops, handlerOp{Kind: handlerOpGroup, Group: pbtPick(r, pbtGroupNames)})
			continue
		}
		attrs := genAttrsWith(r, 1+r.Intn(2), h, r.Intn(4)) // r.Intn(4) can be 0: the empty batch
		ops = append(ops, handlerOp{Kind: handlerOpAttrs, Attrs: attrs})
	}
	return ops
}

// ---------------------------------------------------------------------------
// Counterexample reporting
// ---------------------------------------------------------------------------

// pbtFailf reports a property violation and continues, so one run surfaces
// several counterexamples instead of stopping at the first.
//
// The message carries the seed, the iteration index, and the offending input as
// indented JSON, which is the whole compensation for having no shrinking: the
// input can be replayed with FUNDIS_PBT_SEED and pasted into an example-based
// regression test.
func pbtFailf(t *testing.T, seed int64, iteration int, input any, format string, args ...any) {
	t.Helper()
	t.Errorf("%s\n%s", fmt.Sprintf(format, args...), pbtCounterexample(seed, iteration, input))
}

// pbtFatalf is pbtFailf for a violation that makes the rest of the loop
// meaningless — a corrupted server, an unmarshalable body — and stops the test.
func pbtFatalf(t *testing.T, seed int64, iteration int, input any, format string, args ...any) {
	t.Helper()
	t.Fatalf("%s\n%s", fmt.Sprintf(format, args...), pbtCounterexample(seed, iteration, input))
}

// pbtCounterexample renders the replay coordinates and the input.
func pbtCounterexample(seed int64, iteration int, input any) string {
	return fmt.Sprintf(
		"counterexample: seed %d, iteration %d (replay with %s=%d)\ninput:\n%s",
		seed, iteration, pbtSeedEnv, seed, pbtInputJSON(input),
	)
}

// pbtInputJSON renders a generated input as indented JSON.
//
// It never panics and never fails: a generated input can hold a value whose
// String method panics, a container that references itself, and a type
// encoding/json rejects, so every one of those is described by type name rather
// than formatted, the walk is depth-bounded, and the whole thing sits under a
// recover. A reporting helper that could panic would replace a useful
// counterexample with a stack trace from the reporter.
func pbtInputJSON(input any) string {
	described := pbtDescribe(input)

	raw, err := json.MarshalIndent(described, "", "  ")
	if err != nil {
		return fmt.Sprintf("<unreportable input of type %T: %v>", input, err)
	}
	return string(raw)
}

// pbtDescribe converts a known generated input into a JSON-safe tree. Unknown
// types are passed through, so a test can report its own parameter struct.
func pbtDescribe(input any) (described any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			described = fmt.Sprintf("<panic while describing %T: %v>", input, recovered)
		}
	}()

	switch value := input.(type) {
	case slog.Record:
		return pbtDescribeRecord(value)
	case *slog.Record:
		if value == nil {
			return nil
		}
		return pbtDescribeRecord(*value)
	case slog.Attr:
		return pbtDescribeAttr(value)
	case []slog.Attr:
		return pbtDescribeAttrs(value)
	case slog.Value:
		return pbtDescribeValue(value)
	case handlerOp:
		return pbtDescribeHandlerOp(value)
	case []handlerOp:
		described := make([]any, 0, len(value))
		for _, op := range value {
			described = append(described, pbtDescribeHandlerOp(op))
		}
		return described
	case *Event:
		return pbtDescribeEvent(value)
	case string:
		return pbtSafeString(value)
	case map[string]any:
		return pbtBoundedValue(value, 1)
	case []any:
		return pbtBoundedValue(value, 1)
	default:
		return pbtBoundedValue(input, 1)
	}
}

func pbtDescribeRecord(record slog.Record) any {
	return map[string]any{
		"time":     record.Time.Format(time.RFC3339Nano),
		"level":    record.Level.String(),
		"message":  pbtSafeString(record.Message),
		"pc":       record.PC != 0,
		"numAttrs": record.NumAttrs(),
		"attrs":    pbtDescribeRecordAttrs(record),
	}
}

func pbtDescribeRecordAttrs(record slog.Record) []any {
	attrs := make([]any, 0, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, pbtDescribeAttr(a))
		return true
	})
	return attrs
}

func pbtDescribeAttrs(attrs []slog.Attr) []any {
	described := make([]any, 0, len(attrs))
	for _, a := range attrs {
		described = append(described, pbtDescribeAttr(a))
	}
	return described
}

func pbtDescribeAttr(a slog.Attr) any {
	return map[string]any{"key": pbtSafeString(a.Key), "value": pbtDescribeValue(a.Value)}
}

// pbtDescribeValue describes an attribute value **without resolving it**.
// Resolving would run a generated LogValue method during failure reporting,
// which is the last place to be executing hostile code.
func pbtDescribeValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindGroup:
		return map[string]any{"kind": "Group", "group": pbtDescribeAttrs(v.Group())}
	case slog.KindString:
		return map[string]any{"kind": "String", "value": pbtSafeString(v.String())}
	case slog.KindBool:
		return map[string]any{"kind": "Bool", "value": v.Bool()}
	case slog.KindInt64:
		return map[string]any{"kind": "Int64", "value": v.Int64()}
	case slog.KindUint64:
		return map[string]any{"kind": "Uint64", "value": v.Uint64()}
	case slog.KindFloat64:
		return map[string]any{"kind": "Float64", "value": pbtSafeFloat(v.Float64())}
	case slog.KindDuration:
		return map[string]any{"kind": "Duration", "value": v.Duration().String()}
	case slog.KindTime:
		return map[string]any{"kind": "Time", "value": v.Time().Format(time.RFC3339Nano)}
	default:
		// KindAny and KindLogValuer. The Go type is reported with %T, which is
		// reflection only and calls no method on the value.
		return map[string]any{
			"kind":   v.Kind().String(),
			"goType": fmt.Sprintf("%T", v.Any()),
			"value":  pbtBoundedValue(v.Any(), 1),
		}
	}
}

func pbtDescribeHandlerOp(op handlerOp) any {
	if op.Kind == handlerOpGroup {
		return map[string]any{"op": "WithGroup", "group": pbtSafeString(op.Group)}
	}
	return map[string]any{"op": "WithAttrs", "attrs": pbtDescribeAttrs(op.Attrs)}
}

func pbtDescribeEvent(ev *Event) any {
	if ev == nil {
		return nil
	}

	described := map[string]any{
		"dt":            ev.DT,
		"level":         ev.Level,
		"message":       pbtSafeString(ev.Message),
		"service":       pbtSafeString(ev.Service),
		"environment":   pbtSafeString(ev.Environment),
		"truncated":     ev.Truncated,
		"serializedLen": ev.SerializedLen(),
	}
	if ev.Error != nil {
		chain := make([]string, 0, len(ev.Error.Chain))
		for _, link := range ev.Error.Chain {
			chain = append(chain, pbtSafeString(link))
		}
		described["error"] = map[string]any{
			"message": pbtSafeString(ev.Error.Message),
			"type":    ev.Error.Type,
			"chain":   chain,
		}
	}
	if ev.Source != nil {
		described["source"] = map[string]any{
			"file": ev.Source.File, "line": ev.Source.Line, "function": ev.Source.Function,
		}
	}
	if ev.Request != nil {
		described["request"] = map[string]any{
			"id": pbtSafeString(ev.Request.ID), "method": ev.Request.Method,
			"path": pbtSafeString(ev.Request.Path),
		}
	}
	if ev.User != nil {
		described["user"] = map[string]any{"id": pbtSafeString(ev.User.ID)}
	}
	if ev.Context != nil {
		described["context"] = pbtBoundedValue(ev.Context, 1)
	}
	return described
}

// pbtMaxReportDepth bounds the reporting walk. It exceeds both nesting caps, so
// a legitimately deep counterexample is reported in full, while a cycle is cut
// after a readable number of levels instead of recursing forever.
const pbtMaxReportDepth = 14

// pbtReportStringBytes caps how much of a string a report reproduces. A
// 1 MiB filler in a counterexample would bury the part that matters.
const pbtReportStringBytes = 128

// pbtBoundedValue returns a JSON-safe, depth-bounded rendering of an arbitrary
// value. Every branch either recurses with a larger depth or returns, and the
// depth cap is the only termination guarantee needed for a cyclic input — the
// same argument the Redactor's depth cap rests on.
func pbtBoundedValue(v any, depth int) any {
	if depth > pbtMaxReportDepth {
		return fmt.Sprintf("<deeper than %d levels>", pbtMaxReportDepth)
	}

	switch value := v.(type) {
	case nil:
		return nil
	case string:
		return pbtSafeString(value)
	case map[string]any:
		bounded := make(map[string]any, len(value))
		for key, child := range value {
			bounded[pbtSafeString(key)] = pbtBoundedValue(child, depth+1)
		}
		return bounded
	case []any:
		bounded := make([]any, 0, len(value))
		for _, child := range value {
			bounded = append(bounded, pbtBoundedValue(child, depth+1))
		}
		return bounded
	case json.Number:
		return value.String()
	case float64:
		return pbtSafeFloat(value)
	case error:
		// Reported by type only: a generated error can be self-unwrapping, and
		// its Error method is generated code.
		return fmt.Sprintf("<error %T>", value)
	}

	// Anything else: report it directly only if encoding/json can represent it,
	// so the caller's MarshalIndent cannot fail on a channel, a func, a complex
	// number, or a reference cycle.
	if _, err := json.Marshal(v); err != nil {
		return fmt.Sprintf("<unmarshalable %s>", pbtTypeName(v))
	}
	return v
}

// pbtSafeFloat renders NaN and the infinities as text, since JSON has no
// literal for them and json.Marshal rejects them outright.
func pbtSafeFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%v", f)
	}
	return f
}

// pbtSafeString renders a string as a Go-quoted literal, so invalid UTF-8
// survives the report as escape sequences instead of being replaced, and elides
// the tail of a long one.
func pbtSafeString(s string) string {
	if len(s) <= pbtReportStringBytes {
		return strconv.Quote(s)
	}
	return fmt.Sprintf("%s...(%d bytes total)", strconv.Quote(s[:pbtReportStringBytes]), len(s))
}

// pbtTypeName names a value's type without invoking any of its methods.
func pbtTypeName(v any) string {
	rt := reflect.TypeOf(v)
	if rt == nil {
		return "<nil>"
	}
	return rt.String()
}

// ---------------------------------------------------------------------------
// Harness self-test
// ---------------------------------------------------------------------------
//
// These are not property tests. They assert only that the harness itself is
// usable: that every generator runs pbtIterations times without panicking, that
// it produces varied output rather than the same value repeatedly, that the
// boundary-biased generators actually reach their boundaries, and that
// counterexample reporting survives every hazard a generator can produce.

func TestPBTHarnessSeedDefaultsAndHonoursOverride(t *testing.T) {
	if got := pbtSeedFrom(t, ""); got != pbtDefaultSeed {
		t.Errorf("pbtSeedFrom(\"\") = %d, want the fixed default %d", got, pbtDefaultSeed)
	}
	if got := pbtSeedFrom(t, "-17"); got != -17 {
		t.Errorf("pbtSeedFrom(\"-17\") = %d, want -17", got)
	}

	// Two runs from one seed must draw the same sequence, or no counterexample
	// is replayable.
	first := rand.New(rand.NewSource(pbtDefaultSeed))
	second := rand.New(rand.NewSource(pbtDefaultSeed))
	for i := 0; i < pbtIterations; i++ {
		if a, b := genStatus(first), genStatus(second); a != b {
			t.Fatalf("iteration %d: same seed produced %d and %d", i, a, b)
		}
	}
}

func TestPBTHarnessGeneratorsRunWithoutPanicAndVary(t *testing.T) {
	r, seed := pbtRand(t)

	// Each generator is exercised through pbtInputJSON, which both proves the
	// value can be reported and gives a cheap fingerprint for the variety
	// check. Records are deliberately *not* converted here: convertRecord does
	// not recover panics by contract, so hostile input is Handle's business
	// (Property 3), not the harness's.
	generators := map[string]func() any{
		"genRecord":      func() any { return genRecord(r) },
		"genCleanRecord": func() any { return genRecordWith(r, hazardNone) },
		"genAttr":        func() any { return genAttr(r, genDepth(r)) },
		"genCleanAttr":   func() any { return genAttrWith(r, genDepth(r), hazardNone) },
		"genNestedGroup": func() any { return genNestedGroupAttr(r, genDepth(r), hazardsSerializable) },
		"genEvent":       func() any { return genEvent(r) },
		"genHostileEvent": func() any {
			return genEventWith(r, hazardsAll)
		},
		"genHandlerOps": func() any { return genHandlerOps(r) },
	}

	for name, generate := range generators {
		t.Run(name, func(t *testing.T) {
			distinct := make(map[string]bool, pbtIterations)
			for i := 0; i < pbtIterations; i++ {
				input := generate()

				report := pbtInputJSON(input)
				if !json.Valid([]byte(report)) {
					pbtFatalf(t, seed, i, input, "%s: report is not valid JSON: %s", name, report)
				}
				distinct[report] = true
			}
			if len(distinct) < pbtIterations/2 {
				t.Errorf("%s produced %d distinct values in %d draws, want more variety",
					name, len(distinct), pbtIterations)
			}
		})
	}
}

func TestPBTHarnessHandlerOpsApplyToARealHandler(t *testing.T) {
	r, seed := pbtRand(t)

	// Applied to slog's own JSON handler rather than to this package's Handler,
	// which task 4.1 has not landed yet: handlerOp.apply is defined over the
	// slog.Handler interface precisely so the harness does not depend on it.
	for i := 0; i < pbtIterations; i++ {
		ops := genHandlerOps(r)

		derived := slog.Handler(slog.NewJSONHandler(io.Discard, nil))
		for _, op := range ops {
			derived = op.apply(derived)
		}
		if derived == nil {
			pbtFailf(t, seed, i, ops, "applying handler ops produced a nil handler")
		}
	}
}

func TestPBTHarnessBoundaryGeneratorsReachEveryBoundary(t *testing.T) {
	// A wider loop than pbtIterations, so this stays deterministic under a
	// FUNDIS_PBT_SEED override instead of depending on 100 draws covering 13
	// boundaries.
	const draws = 2000
	r, _ := pbtRand(t)

	cases := []struct {
		name       string
		draw       func() int
		boundaries []int
	}{
		{"genStatus", func() int { return genStatus(r) }, pbtStatusBoundaries},
		{"genDepth", func() int { return genDepth(r) }, pbtDepthBoundaries},
		{"genSize", func() int { return genSize(r) }, pbtSizeBoundaries},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(map[int]int)
			for i := 0; i < draws; i++ {
				seen[tc.draw()]++
			}
			for _, boundary := range tc.boundaries {
				if seen[boundary] == 0 {
					t.Errorf("%s never produced the boundary value %d in %d draws", tc.name, boundary, draws)
				}
			}
			if len(seen) < len(tc.boundaries)+1 {
				t.Errorf("%s produced only %d distinct values; it must also sample off-boundary values",
					tc.name, len(seen))
			}
		})
	}
}

func TestPBTHarnessSizedEventHitsTheExactByteLength(t *testing.T) {
	r, seed := pbtRand(t)

	for i, target := range []int{65535, 65536, 65537, 1 << 20} {
		ev := genSizedEvent(r, target)

		raw, err := json.Marshal(ev)
		if err != nil {
			pbtFatalf(t, seed, i, ev, "genSizedEvent(%d) does not marshal: %v", target, err)
		}
		if len(raw) != target {
			pbtFailf(t, seed, i, ev, "genSizedEvent(%d) marshalled to %d bytes", target, len(raw))
		}
	}

	// Below the minimum encoding the request is unsatisfiable, so the event is
	// returned unpadded rather than the generator silently producing something
	// larger than asked and a size property asserting against the wrong bound.
	small := genSizedEvent(r, 1)
	if filler := small.Context[pbtSizeFillerKey]; filler != "" {
		t.Errorf("genSizedEvent(1) padded the filler to %d bytes, want no padding",
			len(filler.(string)))
	}
}

func TestPBTHarnessReportingSurvivesEveryHazard(t *testing.T) {
	// Every hazard the generators can emit, reported explicitly rather than
	// hoping a random draw includes it. A panic here would mean a real property
	// failure gets replaced by a stack trace from the reporter.
	hazardous := []slog.Attr{
		{},
		slog.Any("nil", nil),
		slog.Any("typed_nil", (*pbtScalarStruct)(nil)),
		slog.String("invalid\xff", "value \xff\xfe"),
		slog.Any("chan", make(chan int)),
		slog.Any("func", func() {}),
		slog.Any("complex", complex(1, 2)),
		slog.Any("chan_struct", pbtChanStruct{Ch: make(chan int)}),
		slog.Any("cyclic_map", pbtCyclicMap()),
		slog.Any("cyclic_slice", pbtCyclicSlice()),
		slog.Any("panic_stringer", pbtPanicStringer{Ch: make(chan int)}),
		slog.Any("panic_logvaluer", pbtPanicLogValuer{}),
		slog.Any("self_error", pbtSelfError{}),
		slog.Float64("nan", math.NaN()),
		slog.Float64("inf", math.Inf(-1)),
		slog.String("large", pbtFiller(1<<20)),
		slog.Group("group", slog.String("inner", "\xff")),
	}

	for _, a := range hazardous {
		report := pbtInputJSON(a)
		if !json.Valid([]byte(report)) {
			t.Errorf("reporting attr %q produced invalid JSON: %s", a.Key, report)
		}
		if strings.Contains(report, strings.Repeat("x", pbtReportStringBytes+1)) {
			t.Errorf("reporting attr %q reproduced an oversized string in full", a.Key)
		}
	}

	// A cyclic event must be reportable too: the redaction depth-cap property
	// generates exactly this and needs a counterexample when it fails.
	cyclic := &Event{DT: "2025-06-14T09:21:44.317Z", Level: "ERROR", Context: pbtCyclicMap()}
	if report := pbtInputJSON(cyclic); !json.Valid([]byte(report)) {
		t.Errorf("reporting a cyclic event produced invalid JSON: %s", report)
	}
}

func TestPBTCounterexampleCarriesSeedAndIteration(t *testing.T) {
	report := pbtCounterexample(1234, 7, slog.String("key", "value"))

	for _, want := range []string{"seed 1234", "iteration 7", pbtSeedEnv + "=1234", `"key"`, `"value"`} {
		if !strings.Contains(report, want) {
			t.Errorf("counterexample report omits %q:\n%s", want, report)
		}
	}
}
