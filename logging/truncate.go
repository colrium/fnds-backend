package logging

// 64 KiB size cap for a serialized Log_Event (Req 3.12). This file is pure: it
// reads no clock, no configuration, and no shipper state, performs no I/O, and
// mutates only the Event it is handed.
//
// Ordering on the Handle path is fixed and load-bearing:
//
//	convert (convertRecord) -> redact (Redactor.Apply) -> size cap
//	(capEventSize) -> enqueue
//
// The cap runs last because it is the only stage that can state the final byte
// length of the payload. Redaction rewrites values — a 4 KiB bearer token
// collapses to the 10-byte [REDACTED] — so measuring before it would truncate
// content that redaction was about to delete anyway, and a length cached before
// it would over-report. Running the cap last also means the cached
// serializedLen recorded here is the exact number of bytes the event
// contributes to a Delivery_Batch body, which is what the delivery goroutine's
// 1 MiB check reads (Req 5.14).

import (
	"encoding/json"
	"sort"
	"unicode/utf8"
)

const (
	// maxEventBytes is the cap from Req 3.12, measured on the *serialized*
	// event: the byte length of its JSON encoding, not the length of any single
	// field. Every decision in this file is made against a real json.Marshal
	// result rather than an estimate, because JSON escaping means raw string
	// length and encoded length differ by an unbounded factor (a string of
	// newlines doubles, a string of control bytes sextuples).
	maxEventBytes = 65536

	// truncationIndicator is appended to every value this file shortens, so a
	// reader of the Better Stack UI can tell a cut value from a value that was
	// genuinely that short (Req 3.12). It is 14 bytes: the horizontal ellipsis
	// U+2026 is 3 bytes of UTF-8 and encoding/json emits it literally.
	truncationIndicator = "…[truncated]"
)

// capEventSize brings ev's serialized form within maxEventBytes and records the
// resulting byte length on ev.
//
// It is called by Handler.Handle after redaction and before enqueue. It always
// calls ev.SetSerializedLen, including on the common path where the event is
// already inside the cap and nothing is modified, so the delivery goroutine
// never has to re-marshal an event to learn its size.
//
// Truncation order is exactly Req 3.12: string values under `context` first, in
// descending order of byte length, then `message`. Two further stages run only
// when those two cannot reach the cap; see reclaimBulk.
//
// Never a truncation candidate, at any stage (Req 3.17): dt, level, service,
// environment, truncated, error.type, and every source field. Their combined
// worst case is bounded — service and environment are capped at 64 characters
// each by conversion, dt is a fixed 24 bytes, level and truncated are a handful
// of bytes, and error.type and the source fields are Go identifiers produced by
// the runtime — so the cap is always reachable without touching them.
func capEventSize(ev *Event) {
	if ev == nil {
		return
	}

	size, ok := serializedSize(ev)
	if !ok {
		// json.Marshal failed, so the event's size is unknowable and cutting
		// strings cannot be shown to help. Conversion normalizes every
		// attribute value into something encoding/json accepts, so this is
		// unreachable from the Handle path; it is reachable for a hand-built
		// Event in an isolation test (Req 12.9). Recording 0 leaves the cache
		// reading as a miss, which SetSerializedLen documents and which makes
		// the delivery goroutine measure the event itself rather than trust an
		// invented number.
		ev.SetSerializedLen(0)
		return
	}
	if size <= maxEventBytes {
		// The overwhelmingly common case: one marshal, no mutation, and
		// truncated is left exactly as conversion and redaction set it, so an
		// event that tripped no cap at all still reports truncated: false
		// (Req 3.14).
		ev.SetSerializedLen(size)
		return
	}

	// Req 3.12 step 5, applied to every event that entered this path, and
	// applied *before* the stages so that every measurement below is taken on
	// the final field set. The latch is not free: `"truncated":false` is one
	// byte longer than `"truncated":true`, so setting it afterwards would leave
	// the cached length one byte above the truth.
	//
	// The latch records that the event exceeded the cap and was subjected to
	// truncation, which is the signal an on-call reader needs: what they are
	// looking at is not the whole record. It is set even in the pathological
	// case where no stage finds anything it can cut — an event driven over
	// 64 KiB by error.type or a source field alone — because reporting
	// truncated: false there would claim a payload is complete when it is over
	// the provider's limit.
	//
	// It is never reset to false: truncated is a latch that the error-chain cap
	// (Req 3.7) and the context depth cap (Req 3.9) may already have set during
	// conversion.
	ev.Truncated = true
	if measured, ok := serializedSize(ev); ok {
		size = measured
	}

	// Stage 1 (Req 3.12): every string under `context`, longest first.
	size = shrinkSlots(ev, collectContextStrings(ev.Context), size)

	// Stage 2 (Req 3.12): the message.
	if size > maxEventBytes {
		size = shrinkSlots(ev, messageSlot(ev), size)
	}

	// Stages 3 and 4: last resorts for bulk that is not a context string and
	// not the message.
	if size > maxEventBytes {
		size = reclaimBulk(ev, size)
	}

	ev.SetSerializedLen(size)
}

// stringSlot is one string-valued position in an Event, with the write-back
// needed to replace it. Slots exist because the truncation order is global —
// longest first across the whole `context` tree, regardless of nesting — so the
// candidates have to be collected and sorted before any of them is cut, and a
// collected candidate must still be writable afterwards.
//
// path is carried for one reason: it makes the sort total. Ranging over a Go
// map is randomized, so length alone would order two equal-length strings
// differently from run to run and make which value got cut depend on chance.
type stringSlot struct {
	path  string
	value string
	set   func(string)
}

// shrinkSlots truncates slots, longest first, until the serialized event fits
// or the slots run out, and returns the resulting serialized size.
//
// size is the event's current serialized length, passed in rather than measured
// so the caller's existing measurement is reused.
//
// Each cut is sized from the *current* overage and the event is re-measured
// after it (Req 3.12), so the first, longest slot normally absorbs the entire
// excess in one step and the loop stops after a single additional marshal.
// Re-measuring rather than subtracting an estimate is what keeps JSON escaping
// from producing an event that is still over the cap after the algorithm
// believes it is done.
func shrinkSlots(ev *Event, slots []stringSlot, size int) int {
	sortSlotsLongestFirst(slots)

	for _, slot := range slots {
		if size <= maxEventBytes {
			return size
		}
		shortened, ok := cutString(slot.value, size-maxEventBytes)
		if !ok {
			// This value is already at or below the length of the indicator, so
			// replacing it would not shrink the payload. Skipped without a
			// re-marshal, which is what keeps an event carrying thousands of
			// tiny context strings from costing thousands of marshals.
			continue
		}
		slot.set(shortened)

		measured, ok := serializedSize(ev)
		if !ok {
			// Unreachable in practice: the event marshalled a moment ago and
			// the only change since was replacing one string with a shorter
			// one. Returning the pre-cut size is the conservative choice — it
			// never under-reports the bytes the event will contribute to a
			// batch.
			return size
		}
		size = measured
	}
	return size
}

// messageSlot returns the single-slot list for `message` (Req 3.12 step 4).
// Returning a slice lets stage 2 reuse shrinkSlots unchanged.
func messageSlot(ev *Event) []stringSlot {
	return []stringSlot{{
		path:  "message",
		value: ev.Message,
		set:   func(s string) { ev.Message = s },
	}}
}

// reclaimBulk is the documented last resort for an event that is still over the
// cap after every context string and the message have been cut.
//
// Req 3.12 names only context strings and `message`, which is sufficient for
// every realistic oversized event, because size in a log payload comes from
// text. It is not sufficient in general: a `context` holding a multi-megabyte
// array of numbers, or a several-megabyte error text, contains no context
// string long enough — or no context string at all — to cut. Rather than loop
// forever on an unreachable target, two bounded stages run:
//
// Stage 3 deletes whole top-level `context` entries, largest serialized value
// first, re-measuring after each. Dropping data is worse than shortening it,
// which is why this runs only after stage 1 has already shortened every string
// it could; the alternative is shipping an event the provider rejects, which
// loses the whole record including the fields that would have explained the
// failure. `truncated` is already about to be set, so the loss is signalled.
//
// Stage 4 shortens the remaining non-privileged strings: error.message, each
// error.chain entry, and the request and user identifiers. error.type is
// excluded, as are all source fields (Req 3.17).
//
// Both stages are finite — stage 3 removes one map entry per iteration, stage 4
// visits a bounded slot list once — so capEventSize terminates on every input.
func reclaimBulk(ev *Event, size int) int {
	size = dropContextEntries(ev, size)
	if size <= maxEventBytes {
		return size
	}
	return shrinkSlots(ev, secondaryStringSlots(ev), size)
}

// dropContextEntries deletes top-level `context` entries, largest serialized
// value first, until the event fits or `context` is empty.
func dropContextEntries(ev *Event, size int) int {
	for len(ev.Context) > 0 && size > maxEventBytes {
		key := largestContextKey(ev.Context)
		delete(ev.Context, key)
		if len(ev.Context) == 0 {
			// An emptied map is already omitted by the omitempty tag, but it is
			// nilled so that "no context" has exactly one representation on the
			// event and a later stage cannot distinguish the two.
			ev.Context = nil
		}

		measured, ok := serializedSize(ev)
		if !ok {
			return size
		}
		size = measured
	}
	return size
}

// largestContextKey returns the key of the context entry whose value has the
// largest JSON encoding, breaking ties by key so the choice is deterministic
// across runs of a randomized map range. A value that fails to marshal counts
// as size 0: it cannot be the reason the event is oversized.
func largestContextKey(context map[string]any) string {
	var (
		chosen  string
		largest = -1
	)
	for key, value := range context {
		size := 0
		if encoded, err := json.Marshal(value); err == nil {
			size = len(encoded)
		}
		if size > largest || (size == largest && key < chosen) {
			chosen, largest = key, size
		}
	}
	return chosen
}

// secondaryStringSlots returns the strings that are neither context strings nor
// `message` but are still not privileged by Req 3.17.
func secondaryStringSlots(ev *Event) []stringSlot {
	slots := make([]stringSlot, 0, 6)

	if ev.Error != nil {
		info := ev.Error
		// error.message only. error.type is privileged (Req 3.17).
		slots = append(slots, stringSlot{
			path:  "error.message",
			value: info.Message,
			set:   func(s string) { info.Message = s },
		})
		for i, link := range info.Chain {
			index := i
			slots = append(slots, stringSlot{
				path:  "error.chain",
				value: link,
				set:   func(s string) { info.Chain[index] = s },
			})
		}
	}

	if ev.Request != nil {
		request := ev.Request
		slots = append(slots,
			stringSlot{path: "request.id", value: request.ID, set: func(s string) { request.ID = s }},
			stringSlot{path: "request.method", value: request.Method, set: func(s string) { request.Method = s }},
			stringSlot{path: "request.path", value: request.Path, set: func(s string) { request.Path = s }},
		)
	}

	if ev.User != nil {
		user := ev.User
		slots = append(slots, stringSlot{path: "user.id", value: user.ID, set: func(s string) { user.ID = s }})
	}

	return slots
}

// collectContextStrings returns every string value reachable under `context`,
// at any nesting depth, including strings held as array elements.
//
// The walk descends map[string]any and []any and treats everything else as a
// leaf, which is exactly the shape conversion produces and the Redactor walks.
// Depth is bounded by maxRedactDepth on the rootDepth axis — the same cap and
// the same axis the Redactor uses — so a cyclic or hundreds-deep hand-built
// event terminates here without a visited set. Anything past that cap has
// already been replaced by [TRUNCATED] on the Handle path, so nothing
// truncatable is missed.
func collectContextStrings(context map[string]any) []stringSlot {
	if len(context) == 0 {
		return nil
	}
	var slots []stringSlot
	// The context object itself sits at rootDepth 1, so its direct children are
	// collected at rootDepth 2 — the ctxLevel + 1 normalization from the design.
	collectStrings(context, "context", rootDepth(1), &slots)
	return slots
}

// collectStrings appends the string slots inside container, which sits at
// rootDepth d.
func collectStrings(container any, path string, d rootDepth, slots *[]stringSlot) {
	if d > maxRedactDepth {
		return
	}

	switch t := container.(type) {
	case map[string]any:
		for key, child := range t {
			childPath := path + "." + key
			if text, ok := child.(string); ok {
				k, m := key, t
				*slots = append(*slots, stringSlot{
					path:  childPath,
					value: text,
					set:   func(s string) { m[k] = s },
				})
				continue
			}
			collectStrings(child, childPath, d+1, slots)
		}
	case []any:
		for i, child := range t {
			childPath := path + "[]"
			if text, ok := child.(string); ok {
				index, list := i, t
				*slots = append(*slots, stringSlot{
					path:  childPath,
					value: text,
					set:   func(s string) { list[index] = s },
				})
				continue
			}
			collectStrings(child, childPath, d+1, slots)
		}
	}
}

// sortSlotsLongestFirst orders slots by descending byte length of their value,
// as Req 3.12 requires, with the path as the tie-break so the order is total.
func sortSlotsLongestFirst(slots []stringSlot) {
	sort.SliceStable(slots, func(i, j int) bool {
		if len(slots[i].value) != len(slots[j].value) {
			return len(slots[i].value) > len(slots[j].value)
		}
		return slots[i].path < slots[j].path
	})
}

// cutString shortens s by at least reduce bytes of content and appends the
// truncation indicator, cutting at a UTF-8 character boundary. It reports false
// when no shortening is possible, in which case s must be left alone.
//
// The reduce budget is charged for the indicator as well as for the content, so
// the returned string is always strictly shorter than s whenever ok is true.
// That is the loop's progress guarantee: a cut that returned a same-length or
// longer string would let shrinkSlots make no headway.
//
// Cutting is safe for Req 3.13: conversion has already replaced every invalid
// byte sequence with the replacement character, and trimPartialRune removes the
// bytes of any rune the cut landed inside, so the result is valid UTF-8 and the
// event still serializes as valid UTF-8 encoded JSON.
func cutString(s string, reduce int) (string, bool) {
	if reduce < 1 {
		reduce = 1
	}
	keep := len(s) - reduce - len(truncationIndicator)
	if keep < 0 {
		keep = 0
	}

	shortened := trimPartialRune(s[:keep]) + truncationIndicator
	if len(shortened) >= len(s) {
		// s is at or below the length of the indicator itself. Replacing it
		// would not reclaim a byte, so it is not a candidate at all.
		return "", false
	}
	return shortened, true
}

// trimPartialRune removes a trailing incomplete UTF-8 sequence from s, so a cut
// taken at an arbitrary byte offset ends on a character boundary (Req 3.12).
//
// Because the input is valid UTF-8, a cut can leave at most three bytes of a
// partial rune, and each of them decodes as RuneError with size 1 — the
// documented signal for an invalid encoding. The size == 0 guard is the
// end-of-string case and is what makes this loop terminate on any input,
// including one whose bytes are all invalid.
func trimPartialRune(s string) string {
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if size == 0 {
			break
		}
		if r == utf8.RuneError && size == 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// serializedSize returns the byte length of ev's JSON encoding, and false when
// ev cannot be marshalled at all.
//
// It measures len(json.Marshal(ev)) — the same encoding the delivery goroutine
// writes into a Delivery_Batch body — so the number cached by capEventSize is
// the event's exact contribution to that body rather than an approximation.
func serializedSize(ev *Event) (int, bool) {
	encoded, err := json.Marshal(ev)
	if err != nil {
		return 0, false
	}
	return len(encoded), true
}
