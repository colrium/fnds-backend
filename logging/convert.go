package logging

// Log_Record to Log_Event conversion. This file is pure: it reads no clock, no
// environment variable, and no shipper state, and it performs no I/O. Every
// input arrives as an argument, which is what makes the whole payload contract
// testable without a Shipper, a queue, or a network (Req 12.9).
//
// Conversion deliberately does not recover panics. A hostile value whose
// LogValue, String, Error, or MarshalJSON method panics is allowed to propagate
// to Handler.Handle, which recovers it, counts one dropped event and one
// recovered conversion panic, and writes one Self_Diagnostic (Req 8.5, 8.8).
// Recovering here instead would hide that accounting.
//
// The one failure mode that must be prevented structurally rather than
// recovered is a stack overflow: it is a fatal runtime error that no recover
// can catch. See fmtSafeType.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// The requirements state two nesting caps measured from different origins, and
// conflating them would either over-truncate the `context` object or leave a
// subtree uncapped. Each origin gets its own unexported type, so applying a cap
// to the wrong axis is a compile error rather than an off-by-one:
//
//	rootDepth(v) = ctxLevel(v) + 1   for any v inside context
//
// appendAttrs can only compare against maxContextDepth, and
// Redactor.redactValue can only compare against maxRedactDepth.

// ctxLevel counts nesting levels below the `context` object. An attribute that
// is a direct child of `context` sits at ctxLevel 1 (Req 3.9).
type ctxLevel int

// rootDepth counts object edges below the Log_Event root. A top-level field
// such as `level`, and the `context` object itself, sits at rootDepth 1
// (Req 10.10).
//
// rootDepth and maxRedactDepth are declared here rather than in redact.go, next
// to the axis and cap they must stay consistent with, so the normalization
// above is reviewable at a glance and the Redactor cannot grow a second,
// drifting copy of the relationship. logging/redact.go consumes both.
type rootDepth int

const (
	// maxContextDepth is the conversion cap (Req 3.9): attributes at ctxLevel 8
	// or shallower are retained; anything at ctxLevel 9 or deeper is omitted and
	// latches `truncated`. Normalized, that means nothing under `context`
	// survives conversion below rootDepth 9.
	maxContextDepth ctxLevel = 8

	// maxRedactDepth is the redaction cap (Req 10.10): a value at rootDepth 11
	// or deeper becomes [TRUNCATED] and is not descended into. Because
	// conversion already bounds `context` at rootDepth 9, this cap is
	// unreachable on the conversion path with one level of headroom. It is a
	// live backstop for hand-built events, for cyclic attribute graphs, and for
	// any future top-level subtree added without a conversion-side cap.
	maxRedactDepth rootDepth = 10

	// rootContextLevel is the ctxLevel of a direct child of `context`, and so
	// the level every top-level appendAttrs call starts from.
	rootContextLevel ctxLevel = 1
)

const (
	// dtLayout formats `dt` as RFC 3339 in UTC with exactly three
	// fractional-second digits (Req 3.2). The trailing Z is appended as a
	// literal by formatTimestamp rather than written into the layout, because Go
	// reads `Z07:00` in a layout as a numeric-offset placeholder and would emit
	// +00:00 for a UTC time in some formats.
	dtLayout = "2006-01-02T15:04:05.000"

	// maxTopLevelFieldChars caps `service` and `environment` at their first 64
	// characters (Req 3.5, 3.15). Characters, not bytes: a multi-byte name is
	// cut on a rune boundary, so the field can never hold a broken sequence.
	maxTopLevelFieldChars = 64

	// unknownFieldValue is the stand-in for an unset or empty service name or
	// environment (Req 3.5, 3.15). Emitting a literal beats emitting an empty
	// string, which is indistinguishable in the Better Stack UI from a field
	// that was never populated.
	unknownFieldValue = "unknown"

	// maxErrorChainLen caps `error.chain` at 10 entries (Req 3.7). The cap is
	// also what makes a self-unwrapping error terminate.
	maxErrorChainLen = 10
)

// Note on what is deliberately *not* capped here: the internal depth of a single
// attribute value. maxContextDepth bounds group nesting, and depth inside one
// value is bounded by the Redactor's rootDepth cap, which is exactly the
// backstop case the design records for it.

// attrFrame binds a batch of preformatted attrs to the group path that was open
// when WithAttrs was called. Frames are immutable once constructed.
//
// It is declared here, beside the conversion that consumes it, rather than in
// handler.go: conversion is the only code that has to understand the binding,
// and keeping the type with its meaning lets the whole payload contract be
// exercised without constructing a Handler. Handler holds a []attrFrame and
// hands it to convertRecord unchanged.
//
// The binding is load-bearing. Storing a flat attr list and applying the
// handler's currently open groups at Handle time would be wrong:
// h.WithAttrs(a).WithGroup("g").WithAttrs(b) must place a at context.a and b at
// context.g.b.
type attrFrame struct {
	groups []string
	attrs  []slog.Attr
}

// convertConfig is the subset of Config that conversion depends on. Conversion
// takes these two strings rather than a whole Config so that it cannot reach a
// clock, a queue, or a token, and so a test can convert a record with one
// literal.
type convertConfig struct {
	ServiceName string
	Environment string
}

// newConvertConfig projects a validated Config onto the fields conversion uses.
func newConvertConfig(cfg Config) convertConfig {
	return convertConfig{ServiceName: cfg.ServiceName, Environment: cfg.Environment}
}

// convertRecord builds the Log_Event for one Log_Record.
//
// frames are the handler-bound attr batches in add order, each carrying the
// group path open when it was added; groups is the group path open at Handle
// time, under which the record's own attrs are nested.
//
// The returned Event is not yet redacted and carries no cached serialized
// length: Redactor.Apply runs next (Req 10.9), and the 64 KiB size cap records
// the length after any truncation it performs.
func convertRecord(ctx context.Context, r slog.Record, cfg convertConfig, frames []attrFrame, groups []string) *Event {
	ev := &Event{
		DT:      formatTimestamp(r.Time),
		Level:   r.Level.String(), // ERROR, and ERROR+2 for an offset level (Req 3.3, 2.7)
		Message: sanitizeString(r.Message),
		Service: fieldOrUnknown(cfg.ServiceName),

		Environment: fieldOrUnknown(cfg.Environment),
		// truncated latches to true from three places — the error-chain cap, the
		// context depth cap, and the later size cap — and is emitted as an
		// explicit false when none of them fires (Req 3.14).
		Truncated: false,
		Source:    sourceFromPC(r.PC),
	}

	// Correlation is read straight from the context the call site passed to
	// slog.ErrorContext. A record emitted through slog.Error carries
	// context.Background(), so both lookups miss and both objects are omitted
	// without the conversion failing (Req 4.5, 4.6, 4.7, 4.10).
	if id, method, path, ok := RequestFromContext(ctx); ok {
		ev.Request = &RequestInfo{
			ID:     sanitizeString(id),
			Method: sanitizeString(method),
			Path:   sanitizeString(path),
		}
	}
	if userID, ok := UserIDFromContext(ctx); ok {
		ev.User = &UserInfo{ID: sanitizeString(userID)}
	}

	// Every attribute lands under `context`, so an attr whose key collides with
	// a reserved top-level key needs no special case at all: `context` is a
	// separate object, and the top-level field is written from the record and
	// the config only (Req 3.10).
	root := make(map[string]any)
	for _, frame := range frames {
		appendAttrs(root, nestUnderGroups(frame.groups, frame.attrs), rootContextLevel, ev)
	}
	if r.NumAttrs() > 0 {
		recordAttrs := make([]slog.Attr, 0, r.NumAttrs())
		r.Attrs(func(a slog.Attr) bool {
			recordAttrs = append(recordAttrs, a)
			return true
		})
		appendAttrs(root, nestUnderGroups(groups, recordAttrs), rootContextLevel, ev)
	}
	if len(root) > 0 {
		ev.Context = root
	}
	return ev
}

// nestUnderGroups wraps attrs in one synthetic slog group per open group name,
// outermost first, so a frame's recorded group path and the groups open at
// Handle time are both expressed as ordinary group attrs.
//
// appendAttrs then needs exactly one nesting code path, which is also the path
// the depth cap, the empty-group rule, and the group-merge rule live on. It
// allocates nothing when no group is open, which is the common case.
func nestUnderGroups(groups []string, attrs []slog.Attr) []slog.Attr {
	for i := len(groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: groups[i], Value: slog.GroupValue(attrs...)}}
	}
	return attrs
}

// appendAttrs writes attrs into dst, the JSON object at ctxLevel lvl below
// `context`, resolving LogValuers and lifting the first error-valued attr onto
// ev.
//
// lvl has type ctxLevel, so the depth comparison below can only be made against
// the conversion cap; comparing it against maxRedactDepth does not compile.
//
// Rules applied here, all of them from the documented slog.Handler contract or
// from Requirement 3:
//
//   - An attr equal to the zero Attr is skipped.
//   - Value.Resolve runs before any inspection, so a LogValuer's replacement
//     value is what gets classified, emitted, and later redacted (Req 10.8,
//     10.9).
//   - A group with an empty key is inlined into the enclosing object; a group
//     whose attr list is empty is dropped, as is a group left empty by the
//     depth cap.
//   - Later duplicate keys at one object level overwrite earlier ones and the
//     key is emitted once, which a map[string]any gives for free (Req 3.11).
//   - Attrs at ctxLevel 9 or deeper are omitted and latch truncated (Req 3.9).
func appendAttrs(dst map[string]any, attrs []slog.Attr, lvl ctxLevel, ev *Event) {
	if len(attrs) == 0 {
		return
	}
	if lvl > maxContextDepth {
		// Everything at this level is nested deeper than 8 levels below
		// `context`. It is omitted, and truncated latches — but only if
		// something that would have been emitted was actually dropped, so an
		// event whose deepest level holds nothing but empty attrs still reports
		// truncated: false (Req 3.14).
		if hasRetainableAttr(attrs) {
			ev.Truncated = true
		}
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
				continue // a group with no attrs is dropped, key and all
			}
			if a.Key == "" {
				// An unnamed group is inlined at the current level, so it adds no
				// nesting and consumes no depth budget.
				appendAttrs(dst, group, lvl, ev)
				continue
			}
			key := sanitizeString(a.Key)
			child := make(map[string]any, len(group))
			if previous, ok := dst[key].(map[string]any); ok {
				// The same group name opened twice — two frames that both called
				// WithGroup("g"), say — merges instead of replacing, so the
				// earlier frame's attrs survive and only colliding keys are
				// overwritten. The previous object is copied rather than written
				// through, because a map that arrived as an attribute value
				// belongs to the call site and the logging path must never
				// mutate it.
				for k, v := range previous {
					child[k] = v
				}
			}
			appendAttrs(child, group, lvl+1, ev)
			if len(child) == 0 {
				continue // every child was empty or beyond the depth cap
			}
			dst[key] = child
			continue
		}

		key := sanitizeString(a.Key)
		if err, ok := errorFromValue(value); ok {
			if ev.Error == nil {
				// The first error-valued attr in traversal order becomes the
				// structured top-level error object and is not repeated under
				// context (Req 3.6).
				ev.Error = errorInfo(err, ev)
				continue
			}
			// Every remaining error-valued attr stays under context as its error
			// text (Req 3.6).
			dst[key] = sanitizeString(err.Error())
			continue
		}
		dst[key] = convertValue(value, ev)
	}
}

// hasRetainableAttr reports whether attrs contains at least one attr that would
// have produced output. It exists so the depth cap latches `truncated` only when
// it actually discards something.
func hasRetainableAttr(attrs []slog.Attr) bool {
	for _, a := range attrs {
		if a.Equal(slog.Attr{}) {
			continue
		}
		if value := a.Value.Resolve(); value.Kind() == slog.KindGroup && len(value.Group()) == 0 {
			continue
		}
		return true
	}
	return false
}

// errorFromValue reports whether a resolved attribute value is a usable error.
//
// A typed nil — a nil *MyError stored in an error interface — is not usable: the
// interface is non-nil, but calling Error on it panics for the overwhelming
// majority of implementations. It is treated as an ordinary value instead, which
// converts to JSON null (Req 3.13) rather than costing the whole record.
func errorFromValue(value slog.Value) (error, bool) {
	if value.Kind() != slog.KindAny {
		return nil, false
	}
	err, ok := value.Any().(error)
	if !ok || isNilError(err) {
		return nil, false
	}
	return err, true
}

// isNilError reports whether err is nil, either as an interface or as a nil
// pointer stored in one.
func isNilError(err error) bool {
	return err == nil || isNilReference(err)
}

// errorInfo builds the structured error object for the lifted error attr.
//
// chain walks errors.Unwrap from outermost to innermost and is emitted only when
// at least one unwrap succeeded, so a leaf error carries no chain at all
// (Req 3.7). The 10-entry cap doubles as the termination guarantee for an error
// that unwraps to itself.
func errorInfo(err error, ev *Event) *ErrorInfo {
	info := &ErrorInfo{
		Message: sanitizeString(err.Error()),
		// The concrete Go type of the error value, e.g. *fmt.wrapError. This is
		// the one place conversion reaches for fmt.
		Type: fmt.Sprintf("%T", err),
	}

	chain := make([]string, 0, 2)
	for current := err; !isNilError(current); current = errors.Unwrap(current) {
		if len(chain) == maxErrorChainLen {
			ev.Truncated = true
			break
		}
		chain = append(chain, sanitizeString(current.Error()))
	}
	if len(chain) > 1 {
		info.Chain = chain
	}
	return info
}

// convertValue turns one resolved, non-group attribute value into something
// encoding/json can represent.
func convertValue(value slog.Value, ev *Event) any {
	switch value.Kind() {
	case slog.KindString:
		return sanitizeString(value.String())
	case slog.KindBool:
		return value.Bool()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return normalizeFloat(value.Float64())
	case slog.KindDuration:
		// A duration is emitted as its Go text form — 1.5s, 250ms — rather than
		// as a nanosecond count, because the value exists to be read by a human
		// in the Better Stack UI.
		return value.Duration().String()
	case slog.KindTime:
		return formatTimestamp(value.Time())
	case slog.KindGroup:
		// Unreachable: appendAttrs handles groups so their nesting, depth cap,
		// and merge behaviour live in one place. Rendered as slog's own group
		// text rather than dropped, so a future caller of convertValue cannot
		// silently lose data.
		return sanitizeString(value.String())
	default:
		// KindAny, plus KindLogValuer which Resolve has already eliminated.
		return normalizeAny(value.Any())
	}
}

// normalizeAny converts an arbitrary Go value into a tree of scalars,
// map[string]any, []any, and json.Number.
//
// The normalization is not cosmetic. The Redactor walks map[string]any and
// []any and treats every other container as a leaf, so a struct or a
// map[string]string left in its original form would hide a sensitive key from
// redaction at every depth below it (Req 10.1, 10.2). Round-tripping through
// encoding/json is what guarantees the walk sees the same shape the wire will.
//
// The round trip also honours MarshalJSON and MarshalText, so a call site keeps
// control of how its own type is rendered, and it collapses invalid UTF-8 to the
// replacement character as a side effect of encoding (Req 3.13). Numbers are
// decoded as json.Number so a large int64 is re-emitted exactly rather than
// through a lossy float64.
func normalizeAny(x any) any {
	if x == nil || isNilReference(x) {
		return nil // Req 3.13: a nil attribute value is JSON null
	}

	// Fast paths for the scalars that reach KindAny — a named string type, a
	// bool-shaped type — so the common case skips the round trip.
	switch t := x.(type) {
	case string:
		return sanitizeString(t)
	case bool:
		return t
	case float64:
		return normalizeFloat(t)
	case float32:
		return normalizeFloat(float64(t))
	}

	encoded, err := json.Marshal(x)
	if err != nil || !utf8.Valid(encoded) {
		// Unsupported types (channels, functions, complex numbers), NaN and
		// infinities nested inside a container, reference cycles, a failing
		// MarshalJSON, and the one case encoding/json does not sanitize — a
		// MarshalJSON that emits invalid UTF-8 — all land here.
		return formatUnrepresentable(x)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return formatUnrepresentable(x)
	}
	return normalized
}

// normalizeFloat keeps a finite float as a JSON number and renders NaN and the
// infinities as their default Go text, since JSON has no literal for them
// (Req 3.13).
func normalizeFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%v", f) // NaN, +Inf, -Inf
	}
	return f
}

// formatUnrepresentable renders a value encoding/json cannot represent, using
// default Go string formatting where that is safe (Req 3.13).
//
// The safety qualifier is not paranoia. fmt applies no cycle detection to maps
// and slices, so formatting a map that contains itself recurses until the
// goroutine stack is exhausted, and a stack overflow is a fatal runtime error
// that Handle's recover cannot catch — it would take the process down over a log
// line. A type that can transitively reach a reference kind is therefore
// described by type name instead of formatted. Channels, functions, complex
// numbers, and structs and arrays of scalars are all formatted normally, which
// covers every value Req 3.13 names.
func formatUnrepresentable(x any) string {
	rt := reflect.TypeOf(x)
	if rt == nil {
		return "<nil>"
	}
	if fmtSafeType(rt, make(map[reflect.Type]bool)) {
		return sanitizeString(fmt.Sprintf("%v", x))
	}
	return "<unrepresentable " + rt.String() + ">"
}

// fmtSafeType reports whether every value of rt is guaranteed to render in
// bounded depth, i.e. whether rt can transitively reach a map, slice, pointer,
// or interface. seen breaks the walk on a recursive type definition.
func fmtSafeType(rt reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[rt] {
		return true
	}
	seen[rt] = true

	switch rt.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
		// A value of any of these kinds can participate in a reference cycle.
		return false
	case reflect.Array:
		return fmtSafeType(rt.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < rt.NumField(); i++ {
			if !fmtSafeType(rt.Field(i).Type, seen) {
				return false
			}
		}
		return true
	default:
		// Scalars, and the reference kinds fmt renders as a bare address without
		// descending into them: chan, func, uintptr, unsafe.Pointer.
		return true
	}
}

// isNilReference reports whether x holds a nil of one of the kinds that can be
// nil. It is what keeps a nil pointer from being classified as an error and from
// being formatted as "<nil>" text when JSON null is the accurate rendering.
func isNilReference(x any) bool {
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

// sourceFromPC resolves the Log_Record program counter into the call site
// (Req 3.8), and returns nil for a record that carries none so the whole
// `source` object is omitted while every required top-level field is still
// emitted (Req 3.16).
//
// file is the base name only: the absolute build path adds no information a
// reader of the Better Stack UI can use and leaks the build machine's directory
// layout.
func sourceFromPC(pc uintptr) *SourceInfo {
	if pc == 0 {
		return nil
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	if frame.File == "" && frame.Function == "" && frame.Line == 0 {
		return nil
	}

	source := &SourceInfo{
		Line:     frame.Line,
		Function: sanitizeString(frame.Function),
	}
	if frame.File != "" {
		// filepath.Base("") is ".", which would be worse than an empty field.
		source.File = sanitizeString(filepath.Base(frame.File))
	}
	return source
}

// formatTimestamp renders t as RFC 3339 in UTC with exactly three
// fractional-second digits (Req 3.2). It is used for `dt` and for every
// time-valued attribute, so one timestamp shape appears throughout a payload.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(dtLayout) + "Z"
}

// fieldOrUnknown caps a configured top-level field at its first 64 characters
// and substitutes `unknown` for an empty one (Req 3.5, 3.15).
func fieldOrUnknown(s string) string {
	s = sanitizeString(s)
	if s == "" {
		return unknownFieldValue
	}
	if utf8.RuneCountInString(s) <= maxTopLevelFieldChars {
		return s
	}
	return string([]rune(s)[:maxTopLevelFieldChars])
}

// sanitizeString replaces every byte sequence that is not valid UTF-8 with the
// Unicode replacement character, so the serialized Log_Event is valid UTF-8
// encoded JSON for every input (Req 3.13).
//
// A valid string is returned untouched and unallocated, which is the case for
// essentially every real log line.
func sanitizeString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}
