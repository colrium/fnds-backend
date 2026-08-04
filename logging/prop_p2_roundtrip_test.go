package logging

// Property 2 for the betterstack-error-logging feature: conversion round-trips
// through JSON.
//
// This is the highest-value property in the feature, because the feature is
// fundamentally a serializer: if the payload contract holds for arbitrary
// Log_Records, everything downstream — redaction, size capping, batching — is
// operating on well-formed input.
//
// The oracle is the source slog.Record itself, per the design's P2 row: convert
// it, marshal it, decode the bytes into a generic map, and walk the key paths a
// reference model says the attributes must be recoverable at. The model below is
// a deliberately separate, second implementation of Requirement 3's placement
// rules — depth cap, group inlining, group merging, last-key-wins, error lifting
// — so a single mistake in convert.go cannot be mirrored by the expectation.
//
// It lives in its own file rather than in event_test.go so it stays independent
// of the other conversion tests, and it declares no helper that another test
// file already owns: testConvertConfig, newTestRecord, convertForTest and
// contextOf come from convert_test.go, marshalEvent from event_test.go, and the
// whole pbt*/hazard*/gen* harness from pbt_test.go. Every identifier introduced
// here is p2-prefixed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// p2RequiredTopLevelFields are the six fields every Log_Event carries
// (Req 3.1). truncated is included: it is emitted as an explicit false, so its
// presence is part of the contract rather than an artefact of a non-zero value.
var p2RequiredTopLevelFields = []string{"dt", "level", "message", "service", "environment", "truncated"}

// p2DTPattern is RFC 3339 in UTC with exactly three fractional-second digits and
// a literal Z (Req 3.2). Written out as a pattern rather than checked by parsing
// alone, because time.Parse accepts one, six, or nine fractional digits and a
// numeric offset, all of which this field must never be.
var p2DTPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// p2MaxContextLevel is the conversion depth cap from Req 3.9, restated as a
// literal on purpose: the model must fail if the production constant drifts from
// the requirement, which it cannot do if it reads that constant.
const p2MaxContextLevel = 8

// p2MaxFieldChars is the `service`/`environment` cap from Req 3.5 and 3.15,
// restated for the same reason.
const p2MaxFieldChars = 64

// p2ServiceNames and p2Environments exercise the capping and fallback rules of
// Req 3.5 and 3.15: unset, ordinary, whitespace-only (which conversion does not
// trim — trimming is the config layer's job), far over the 64-character cap in
// both ASCII and multi-byte runes, and invalid UTF-8.
var p2ServiceNames = []string{
	"", "fundis-backend", " ", strings.Repeat("s", 200), strings.Repeat("é", 90), "svc \xff",
}

var p2Environments = []string{
	"", "production", "staging", "development", strings.Repeat("e", 100), "env \xff",
}

// Feature: betterstack-error-logging, Property 2: Conversion round-trips through JSON
// **Validates: Requirements 3.1, 3.2, 3.3, 3.4, 3.5, 3.13, 3.15**
//
// For any Log_Record, decoding the serialized Log_Event yields `message`,
// `level`, `dt`, `service`, and `environment` equal to the corresponding
// Log_Record field or configured value, and every non-redacted, non-truncated
// attribute value is recoverable at its expected key path.
func TestProperty2_ConversionRoundTripsThroughJSON(t *testing.T) {
	r, seed := pbtRand(t)

	for i := 0; i < pbtIterations; i++ {
		// Alternating hazard sets: hazardNone is the case whose expected output
		// the model can state exactly, and hazardsSerializable adds the nils,
		// invalid UTF-8, empty keys, and boundary-sized strings that Req 3.13
		// names while staying marshalable — which conversion must also place
		// correctly.
		h := hazardNone
		if i%2 == 1 {
			h = hazardsSerializable
		}

		record := genRecordWith(r, h)
		if pbtChance(r, 40) {
			// An attribute nested to an exact depth, so the run straddles the
			// cap — a leaf at ctxLevel 8 must be recoverable, one at 9 must be
			// absent (Req 3.9) — rather than relying on genRecord's depth budget
			// happening to land there.
			record.AddAttrs(genNestedGroupAttr(r, pbtPick(r, pbtDepthBoundaries), h))
		}
		cfg := convertConfig{
			ServiceName: pbtPick(r, p2ServiceNames),
			Environment: pbtPick(r, p2Environments),
		}

		p2CheckRoundTrip(t, seed, i, record, cfg)
	}
}

// p2CheckRoundTrip converts one record, decodes the serialized event, and
// asserts every clause of Property 2 against it.
func p2CheckRoundTrip(t *testing.T, seed int64, iteration int, record slog.Record, cfg convertConfig) {
	t.Helper()

	input := p2Report(record, cfg)
	ev := convertRecord(context.Background(), record, cfg, nil, nil)

	raw, err := json.Marshal(ev)
	if err != nil {
		pbtFatalf(t, seed, iteration, input, "json.Marshal(Event) returned error: %v", err)
	}

	// Req 3.13: the emitted bytes are valid UTF-8 encoded JSON.
	if !utf8.Valid(raw) {
		pbtFailf(t, seed, iteration, input, "serialized Log_Event is not valid UTF-8: %q", raw)
	}
	if !json.Valid(raw) {
		pbtFailf(t, seed, iteration, input, "serialized Log_Event is not valid JSON: %q", raw)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		pbtFatalf(t, seed, iteration, input, "serialized Log_Event does not decode as an object: %v (%s)", err, raw)
	}

	// Req 3.1: all six required top-level fields survive the round trip.
	for _, field := range p2RequiredTopLevelFields {
		if _, ok := decoded[field]; !ok {
			pbtFailf(t, seed, iteration, input, "required top-level field %q missing from %s", field, raw)
		}
	}

	p2CheckDT(t, seed, iteration, input, decoded, record)
	p2CheckStringField(t, seed, iteration, input, decoded, "level", record.Level.String())          // Req 3.3
	p2CheckStringField(t, seed, iteration, input, decoded, "message", p2Sanitize(record.Message))   // Req 3.4
	p2CheckStringField(t, seed, iteration, input, decoded, "service", p2Field(cfg.ServiceName))     // Req 3.5
	p2CheckStringField(t, seed, iteration, input, decoded, "environment", p2Field(cfg.Environment)) // Req 3.15

	if _, ok := decoded["truncated"].(bool); !ok {
		pbtFailf(t, seed, iteration, input, "truncated = %#v, want a JSON boolean", decoded["truncated"])
	}

	p2CheckAttributes(t, seed, iteration, input, decoded, record)
}

// p2CheckDT asserts the `dt` shape and value (Req 3.2).
func p2CheckDT(t *testing.T, seed int64, iteration int, input any, decoded map[string]any, record slog.Record) {
	t.Helper()

	dt, ok := decoded["dt"].(string)
	if !ok {
		pbtFailf(t, seed, iteration, input, "dt = %#v, want a JSON string", decoded["dt"])
		return
	}
	if !p2DTPattern.MatchString(dt) {
		pbtFailf(t, seed, iteration, input,
			"dt = %q, want RFC 3339 in UTC with exactly three fractional digits", dt)
		return
	}

	parsed, err := time.Parse(time.RFC3339, dt)
	if err != nil {
		pbtFailf(t, seed, iteration, input, "dt = %q does not parse as RFC 3339: %v", dt, err)
		return
	}
	// Formatting with three fractional digits truncates rather than rounds, so
	// the millisecond truncation of the record timestamp is the exact expectation.
	if want := record.Time.UTC().Truncate(time.Millisecond); !parsed.Equal(want) {
		pbtFailf(t, seed, iteration, input, "dt = %q, want the record timestamp %s truncated to milliseconds",
			dt, want.Format(time.RFC3339Nano))
	}
}

// p2CheckStringField asserts one decoded top-level string field.
func p2CheckStringField(
	t *testing.T, seed int64, iteration int, input any,
	decoded map[string]any, field, want string,
) {
	t.Helper()

	got, ok := decoded[field].(string)
	if !ok {
		pbtFailf(t, seed, iteration, input, "%s = %#v, want a JSON string", field, decoded[field])
		return
	}
	if got != want {
		pbtFailf(t, seed, iteration, input, "%s = %q, want %q", field, got, want)
	}
}

// p2CheckAttributes asserts that every attribute the record carried is
// recoverable at the key path the reference model computes for it, and that the
// three documented exceptions hold: the first error-valued attribute is lifted
// to `error`, attributes past the depth cap are absent, and a later duplicate
// key overwrites an earlier one.
func p2CheckAttributes(
	t *testing.T, seed int64, iteration int, input any,
	decoded map[string]any, record slog.Record,
) {
	t.Helper()

	model := p2BuildModel(record)

	// Exception 1: the first error-valued attribute becomes the top-level
	// `error` object instead of a `context` entry (Req 3.6, asserted here only
	// as far as the round trip depends on it).
	errorObject, hasErrorObject := decoded["error"].(map[string]any)
	switch {
	case model.errorLifted && !hasErrorObject:
		pbtFailf(t, seed, iteration, input, "record carries an error-valued attr but no `error` object was emitted")
	case !model.errorLifted && hasErrorObject:
		pbtFailf(t, seed, iteration, input, "`error` object emitted for a record carrying no error-valued attr: %#v", errorObject)
	case model.errorLifted:
		if got, _ := errorObject["message"].(string); got != model.errorMessage {
			pbtFailf(t, seed, iteration, input, "error.message = %q, want %q", got, model.errorMessage)
		}
	}

	leaves := model.leaves()
	contextObject, hasContext := decoded["context"].(map[string]any)

	if len(leaves) == 0 {
		// Everything the record carried was a zero Attr, an empty group, a
		// lifted error, or past the depth cap, so `context` is omitted entirely.
		if hasContext {
			pbtFailf(t, seed, iteration, input,
				"`context` emitted as %#v, want it omitted: no attribute survives conversion", contextObject)
		}
		return
	}
	if !hasContext {
		pbtFailf(t, seed, iteration, input, "`context` omitted, want %d recoverable attribute value(s)", len(leaves))
		return
	}

	for _, leaf := range leaves {
		want, ok := p2Canonical(leaf.value)
		if !ok {
			// The generated value is outside the round-trippable input space the
			// model can state an expectation for. Asserting on it would test the
			// model, not the code.
			continue
		}

		got, found := p2Lookup(contextObject, leaf.path)
		if !found {
			pbtFailf(t, seed, iteration, input,
				"attribute value not recoverable at context.%s; context = %#v", leaf.pathString(), contextObject)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			pbtFailf(t, seed, iteration, input,
				"context.%s = %#v, want %#v", leaf.pathString(), got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Reference model
// ---------------------------------------------------------------------------

// p2Object is a model-built JSON object, i.e. one produced from a slog group.
//
// The distinct type is load-bearing: an attribute value can itself normalize to
// a map[string]any, and a group must not merge into one of those. Only a
// p2Object is a merge target, which is how the model expresses "a group opened
// twice merges; anything else is overwritten".
type p2Object map[string]any

// p2Model is the expected placement of one record's attributes.
type p2Model struct {
	root         p2Object
	errorLifted  bool
	errorMessage string
}

// p2Leaf is one expected value together with the key path under `context` it
// must be recoverable at.
type p2Leaf struct {
	path  []string
	value any
}

func (l p2Leaf) pathString() string { return strings.Join(l.path, ".") }

// p2BuildModel walks the record's attributes and computes where each one must
// land, applying Requirement 3's placement rules independently of convert.go:
//
//   - the zero Attr is ignored, and a LogValuer is resolved before inspection;
//   - a group with no attrs is dropped, and a group with an empty key is inlined
//     at the current level without consuming depth budget;
//   - a group name opened twice merges; any other repeated key is overwritten by
//     the later attribute (Req 3.11);
//   - an attribute at a level deeper than 8 below `context` is omitted (Req 3.9);
//   - the first error-valued attribute in traversal order is lifted out, and
//     every later one is placed as its error text (Req 3.6).
func p2BuildModel(record slog.Record) *p2Model {
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	model := &p2Model{root: p2Object{}}
	model.appendAttrs(model.root, attrs, 1)
	return model
}

// appendAttrs places attrs into dst, the object at level lvl below `context`.
// Depth-first and in order, which is what makes "the first error-valued attr"
// well defined for a nested group.
func (m *p2Model) appendAttrs(dst p2Object, attrs []slog.Attr, lvl int) {
	if len(attrs) == 0 || lvl > p2MaxContextLevel {
		return
	}

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
				m.appendAttrs(dst, group, lvl)
				continue
			}

			key := p2Sanitize(a.Key)
			child, merging := dst[key].(p2Object)
			if !merging {
				child = p2Object{}
			}
			m.appendAttrs(child, group, lvl+1)
			if len(child) == 0 {
				continue
			}
			dst[key] = child
			continue
		}

		key := p2Sanitize(a.Key)
		if err, ok := p2ErrorValue(value); ok {
			if !m.errorLifted {
				m.errorLifted = true
				m.errorMessage = p2Sanitize(err.Error())
				continue
			}
			dst[key] = p2Sanitize(err.Error())
			continue
		}
		dst[key] = p2Value(value)
	}
}

// leaves flattens the model into the expected (key path, value) pairs.
func (m *p2Model) leaves() []p2Leaf {
	var collected []p2Leaf
	p2Collect(m.root, nil, &collected)
	return collected
}

func p2Collect(object p2Object, prefix []string, out *[]p2Leaf) {
	for key, value := range object {
		path := append(append(make([]string, 0, len(prefix)+1), prefix...), key)
		if child, ok := value.(p2Object); ok {
			// An object left empty by the depth cap contributes no expectation,
			// and conversion drops it rather than emitting {}.
			if len(child) > 0 {
				p2Collect(child, path, out)
			}
			continue
		}
		*out = append(*out, p2Leaf{path: path, value: value})
	}
}

// p2Value is the expected JSON rendering of one resolved, non-group attribute
// value, stated from Requirement 3 rather than from convertValue.
func p2Value(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return p2Sanitize(value.String())
	case slog.KindBool:
		return value.Bool()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return p2Float(value.Float64())
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return p2Timestamp(value.Time())
	default:
		return p2Any(value.Any())
	}
}

// p2Any is the expected rendering of a KindAny value: JSON null for a nil,
// sanitized text for a string, and otherwise whatever encoding/json makes of the
// value — which is the normalization Req 3.13 describes and the shape the wire
// carries.
func p2Any(x any) any {
	if x == nil || p2IsNilReference(x) {
		return nil
	}

	switch t := x.(type) {
	case string:
		return p2Sanitize(t)
	case bool:
		return t
	case float64:
		return p2Float(t)
	case float32:
		return p2Float(float64(t))
	}

	raw, err := json.Marshal(x)
	if err != nil {
		// Not round-trippable, so the model states no expectation for it. Only
		// hazards this property does not enable can reach here.
		return p2Unassertable{}
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return p2Unassertable{}
	}
	return normalized
}

// p2Unassertable marks a value the model cannot state an expectation for.
// p2Canonical rejects it, and the assertion loop skips the leaf.
type p2Unassertable struct{}

// p2Float keeps a finite float as a number and renders NaN and the infinities as
// their Go text, since JSON has no literal for them (Req 3.13).
func p2Float(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%v", f)
	}
	return f
}

// p2Timestamp renders a time-valued attribute the way Req 3.2 renders `dt`.
func p2Timestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// p2Sanitize replaces every byte sequence that is not valid UTF-8 with the
// Unicode replacement character (Req 3.13).
func p2Sanitize(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// p2Field applies the 64-character cap and the `unknown` fallback to a
// configured top-level field (Req 3.5, 3.15). Sanitization runs first, so the
// cut lands on a rune boundary of the sanitized value.
func p2Field(s string) string {
	s = p2Sanitize(s)
	if s == "" {
		return "unknown"
	}
	runes := []rune(s)
	if len(runes) <= p2MaxFieldChars {
		return s
	}
	return string(runes[:p2MaxFieldChars])
}

// p2ErrorValue reports whether a resolved value is a usable error. A nil
// pointer stored in an error interface is not: calling Error on it panics, so it
// is an ordinary value that renders as JSON null.
func p2ErrorValue(value slog.Value) (error, bool) {
	if value.Kind() != slog.KindAny {
		return nil, false
	}
	err, ok := value.Any().(error)
	if !ok || err == nil || p2IsNilReference(err) {
		return nil, false
	}
	return err, true
}

// p2IsNilReference reports whether x holds a nil of one of the nilable kinds.
func p2IsNilReference(x any) bool {
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Comparison helpers
// ---------------------------------------------------------------------------

// p2Canonical renders an expected value in the same form json.Unmarshal produces
// for the emitted event, so a large int64 and a json.Number of the same digits
// compare equal instead of differing by Go type.
func p2Canonical(v any) (any, bool) {
	if _, unassertable := v.(p2Unassertable); unassertable {
		return nil, false
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return nil, false
	}
	return canonical, true
}

// p2Lookup resolves a key path inside the decoded `context` object.
func p2Lookup(contextObject map[string]any, path []string) (any, bool) {
	current := any(contextObject)
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[segment]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// p2Report renders the generated input for a counterexample: the record through
// the harness's own hostile-value-safe describer, plus the config, since the
// service and environment expectations depend on it.
func p2Report(record slog.Record, cfg convertConfig) any {
	return map[string]any{
		"record":      pbtDescribe(record),
		"service":     pbtSafeString(cfg.ServiceName),
		"environment": pbtSafeString(cfg.Environment),
	}
}
