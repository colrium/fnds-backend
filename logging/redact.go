package logging

import "strings"

// Redactor removes credentials from a Log_Event after conversion and before
// serialization and enqueue, so the Event_Queue never holds an unredacted event
// (Req 10.9).
//
// Selection is deliberately narrow (Req 10.11): a value is replaced when
//
//  1. at least one segment of its key path contains a sensitive substring, or
//  2. the value is a string equal to the Source_Token.
//
// There is no content-shape inspection at all — no `Bearer ` prefix sniffing,
// no JWT-shape matching, no entropy heuristic. Shape heuristics produce false
// positives on ordinary identifiers and false negatives on the next credential
// format, and neither failure mode is observable from a log payload after the
// fact.
//
// Req 10.6 — no personal names, email addresses, or phone numbers in any
// Log_Record — is deliberately *not* enforced here. The Redactor matches keys,
// so it cannot recognise a personal name that arrives under an innocuous key.
// That requirement stays a call-site discipline asserted by
// notifications/activity_test.go.
type Redactor struct {
	// sourceToken is compared by equality only (Req 10.11). An empty token
	// disables the equality check entirely; see equalsSourceToken.
	sourceToken string
}

// NewRedactor returns a Redactor that replaces values matching a sensitive key
// segment and values equal to sourceToken.
//
// An empty sourceToken is valid and means "no token to match": the Log_Shipper
// is in disabled state in that case (Req 1.4), but a Handler can still be
// constructed and exercised without a Shipper (Req 12.9), and the Redactor must
// behave sanely there.
func NewRedactor(sourceToken string) *Redactor {
	return &Redactor{sourceToken: sourceToken}
}

const (
	// redactedPlaceholder is the single JSON string that replaces a redacted
	// value, whatever the original type was (Req 10.3).
	redactedPlaceholder = "[REDACTED]"
	// truncatedPlaceholder is the single JSON string that replaces a value
	// nested deeper than the redaction depth cap (Req 10.10).
	truncatedPlaceholder = "[TRUNCATED]"
)

// sensitiveSubstrings are matched case-insensitively against each key path
// segment (Req 10.1). They are substrings rather than whole keys so that
// real-world key spellings are covered without enumerating them: access_key_id
// matches on access_key, refresh_token and id_token match on token,
// set-cookie matches on cookie.
//
// "key" is listed on its own, not only as part of "api_key"/"access_key"/
// "private_key", so that any *_key spelling redacts — encryption_key,
// signing_key, webhook_key, hmac_key — without enumerating every suffix a
// caller might choose. A false positive here (a non-secret field that merely
// contains "key") costs an unnecessary [REDACTED]; a false negative costs a
// leaked credential, so this list is deliberately biased toward
// over-matching.
var sensitiveSubstrings = []string{
	"token", "secret", "password", "passwd", "authorization",
	"key",
	"session", "cookie", "jwt", "bearer",
}

// Apply redacts ev in place.
//
// Scope (Req 10.2): context, error, request, and user are walked at every
// nesting depth. dt, level, message, service, environment, source, and
// truncated are left byte-for-byte unmodified — they are shipper-generated
// fields, not caller-supplied attribute values, so there is nothing there for
// a caller to leak a credential into.
//
// Apply is idempotent (Property 4). Every decision it makes is a function of
// the key path and of the value's equality with the Source_Token, and the
// replacement values [REDACTED] and [TRUNCATED] are stable under both rules:
// a key that matched still matches on a second pass and is replaced with the
// same string, and a subtree that was truncated no longer contains anything to
// descend into. So Apply(Apply(ev)) == Apply(ev).
//
// Apply never sets Truncated back to false: truncated is a latch that
// conversion (Req 3.7, 3.9) and the size cap (Req 3.12) also set.
func (r *Redactor) Apply(ev *Event) {
	if ev == nil {
		return
	}

	// Each top-level field of the Event sits at rootDepth 1, so the direct
	// children of context start at rootDepth 2 — exactly ctxLevel + 1, the
	// normalization documented in the design's depth-cap table.
	if ev.Context != nil {
		v, truncated := r.redactValue(ev.Context, rootDepth(1))
		if m, ok := v.(map[string]any); ok {
			ev.Context = m
		} else {
			// Only reachable if the depth cap or a key match replaced the
			// whole context object, which cannot happen at rootDepth 1 with
			// the key "context". Handled rather than asserted so a future
			// change cannot silently ship an unredacted map.
			ev.Context = map[string]any{"context": v}
		}
		if truncated {
			ev.Truncated = true
		}
	}

	// error, request, and user are fixed-shape structs, not maps, so the walk
	// over them is unrolled rather than recursive. Their JSON keys are
	// message/type/chain, id/method/path, and id: none of those contains a
	// sensitive substring, so in practice the key rule never fires here. The
	// key check is applied anyway, per field, so that renaming a field to
	// something like session_id starts redacting it instead of silently
	// shipping it.
	//
	// The Source_Token equality rule (Req 10.11) genuinely does apply to these
	// values: an error text is frequently built from a failed request and can
	// carry a credential verbatim.
	if ev.Error != nil {
		ev.Error.Message = r.redactField("message", ev.Error.Message)
		ev.Error.Type = r.redactField("type", ev.Error.Type)
		for i, link := range ev.Error.Chain {
			// The chain elements are array members with no key of their own,
			// so they are tested against their containing key, "chain".
			ev.Error.Chain[i] = r.redactField("chain", link)
		}
	}

	if ev.Request != nil {
		ev.Request.ID = r.redactField("id", ev.Request.ID)
		ev.Request.Method = r.redactField("method", ev.Request.Method)
		ev.Request.Path = r.redactField("path", ev.Request.Path)
	}

	if ev.User != nil {
		ev.User.ID = r.redactField("id", ev.User.ID)
	}
}

// redactValue returns the redacted form of a value that sits at rootDepth d,
// together with whether anything beneath it was replaced by the depth cap.
//
// The rootDepth type keeps this comparison on the correct axis: d is measured
// from the Event root, so it can only be compared against maxRedactDepth, and
// comparing it against the conversion cap maxContextDepth does not compile.
//
// Values at rootDepth >= 11 become [TRUNCATED] and are not descended into
// (Req 10.10). That cap is the *only* termination guarantee this walk needs:
// a self-referential or mutually-recursive attribute graph is bounded by depth
// rather than by a visited set, so no per-node bookkeeping is allocated and a
// cyclic event terminates in at most 10 levels of recursion (Property 14).
//
// On the conversion path the cap is unreachable: conversion already caps
// context at ctxLevel 8, which normalizes to rootDepth 9, one level of
// headroom. It is a live backstop for hand-built events passed straight to the
// Redactor in isolation tests (Req 12.9) and for any future top-level subtree
// added without a conversion-side cap.
func (r *Redactor) redactValue(v any, d rootDepth) (any, bool) {
	if d > maxRedactDepth {
		return truncatedPlaceholder, true
	}

	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			return v, false
		}
		// A fresh map is built rather than mutating in place, so a value
		// reachable by two paths, or by a cycle, cannot be observed
		// half-redacted while the walk is still running.
		out := make(map[string]any, len(t))
		truncated := false
		for key, child := range t {
			// A map key is exactly one key path segment, because conversion
			// represents a slog.Group as a nested map rather than as a dotted
			// key. Matching therefore never sees a joined path such as
			// auth.id, which would match nothing, nor treats a benign parent
			// name as part of a child's key (Req 10.1).
			if matchesSensitiveKey(key) {
				// The whole value goes, whatever its type: string, number,
				// bool, null, object, or array, plus everything nested
				// beneath it, collapses to the single string [REDACTED].
				// The key itself is preserved, so the shape of the payload
				// still shows that the attribute was present (Req 10.3).
				out[key] = redactedPlaceholder
				continue
			}
			redacted, childTruncated := r.redactValue(child, d+1)
			out[key] = redacted
			truncated = truncated || childTruncated
		}
		return out, truncated

	case []any:
		if t == nil {
			return v, false
		}
		out := make([]any, len(t))
		truncated := false
		for i, child := range t {
			// Array elements carry no key of their own. The containing key was
			// already tested by the parent map, so an element is only subject
			// to the depth cap and the Source_Token equality rule.
			redacted, childTruncated := r.redactValue(child, d+1)
			out[i] = redacted
			truncated = truncated || childTruncated
		}
		return out, truncated

	case string:
		if r.equalsSourceToken(t) {
			return redactedPlaceholder, false
		}
		return t, false

	default:
		// Numbers, bools, nil, and any other leaf are returned unchanged. They
		// cannot equal the Source_Token, which is a string, and no content
		// inspection is performed on them (Req 10.11).
		//
		// Container types other than map[string]any and []any are leaves here
		// by design: conversion normalizes every attribute value into a
		// scalar, a map[string]any, or a []any before the Redactor runs, so
		// those two cases cover every container the conversion path can
		// produce.
		return v, false
	}
}

// redactField applies both selection rules to one string field of a
// fixed-shape struct such as ErrorInfo or RequestInfo. Those structs are at
// rootDepth 1 and their fields at rootDepth 2, far inside the depth cap, so no
// depth argument is needed.
func (r *Redactor) redactField(key, value string) string {
	if matchesSensitiveKey(key) || r.equalsSourceToken(value) {
		return redactedPlaceholder
	}
	return value
}

// matchesSensitiveKey reports whether one key path segment contains a sensitive
// substring, compared case-insensitively (Req 10.1).
func matchesSensitiveKey(segment string) bool {
	lowered := strings.ToLower(segment)
	for _, substring := range sensitiveSubstrings {
		if strings.Contains(lowered, substring) {
			return true
		}
	}
	return false
}

// equalsSourceToken reports whether value is the Source_Token (Req 10.11).
//
// The empty-token guard is load-bearing, not defensive: the token is empty
// whenever BETTERSTACK_SOURCE_TOKEN is unset, which is the normal state in
// tests and in local development (Req 12.3). Without the guard, every empty
// string in every event would be replaced with [REDACTED], which would corrupt
// the payload precisely where nothing is at risk.
func (r *Redactor) equalsSourceToken(value string) bool {
	return r.sourceToken != "" && value == r.sourceToken
}
