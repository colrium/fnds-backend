package logging

import (
	"reflect"
	"testing"
)

// Focused example-based coverage of the Redactor. The universal properties —
// idempotence (P4), structure preservation (P5), and depth-cap termination
// (P14) — are covered by the property tests in tasks 2.10 through 2.12; these
// cases pin the specific behaviours those properties are easy to satisfy
// vacuously.

func TestRedactorMatchesPerKeySegmentNotJoinedPath(t *testing.T) {
	r := NewRedactor("")

	ev := &Event{
		Context: map[string]any{
			// Substring match inside a longer segment (Req 10.1).
			"access_key_id": "AKIAEXAMPLE",
			"Authorization": "Bearer abc",
			"refresh_token": "rt-1",
			// A benign segment under a benign parent stays put. If matching
			// used the joined path "auth.id", neither segment would match
			// either, but the group name must not leak into the child's key.
			"auth": map[string]any{
				"id":         "user-7",
				"session_id": "s-1",
			},
			"user_id": "user-7",
		},
	}

	r.Apply(ev)

	want := map[string]any{
		"access_key_id": redactedPlaceholder,
		"Authorization": redactedPlaceholder,
		"refresh_token": redactedPlaceholder,
		"auth": map[string]any{
			"id":         "user-7",
			"session_id": redactedPlaceholder,
		},
		"user_id": "user-7",
	}
	if !reflect.DeepEqual(ev.Context, want) {
		t.Errorf("context = %#v, want %#v", ev.Context, want)
	}
}

func TestRedactorReplacesWholeValueRegardlessOfType(t *testing.T) {
	r := NewRedactor("")

	ev := &Event{
		Context: map[string]any{
			"token_count":   42,
			"has_secret":    true,
			"password":      nil,
			"cookie_jar":    map[string]any{"a": map[string]any{"b": "c"}},
			"jwt_list":      []any{"one", "two"},
			"session_empty": map[string]any{},
		},
	}

	r.Apply(ev)

	for _, key := range []string{"token_count", "has_secret", "password", "cookie_jar", "jwt_list", "session_empty"} {
		got, present := ev.Context[key]
		if !present {
			t.Errorf("key %q was deleted; keys must be preserved (Req 10.3)", key)
			continue
		}
		if got != redactedPlaceholder {
			t.Errorf("context[%q] = %#v, want %q", key, got, redactedPlaceholder)
		}
	}
}

func TestRedactorReplacesSourceTokenByEqualityOnly(t *testing.T) {
	const token = "st_live_abcdef123456"
	r := NewRedactor(token)

	ev := &Event{
		Message: token,
		Context: map[string]any{
			"upstream_note": token,
			"nested":        map[string]any{"list": []any{token, "other"}},
			// No content-shape inspection: a Bearer-prefixed or JWT-shaped
			// string that is not the token itself survives (Req 10.11).
			"header_sample": "Bearer " + token,
			"shaped_value":  "aaa.bbb.ccc",
		},
		Error:   &ErrorInfo{Message: "post failed: " + token, Type: "*url.Error", Chain: []string{token, "dial tcp"}},
		Request: &RequestInfo{ID: "req-1", Method: "POST", Path: "/v1/logs"},
		User:    &UserInfo{ID: token},
	}

	r.Apply(ev)

	if got := ev.Context["upstream_note"]; got != redactedPlaceholder {
		t.Errorf("context[upstream_note] = %#v, want %q", got, redactedPlaceholder)
	}
	nested := ev.Context["nested"].(map[string]any)["list"].([]any)
	if nested[0] != redactedPlaceholder || nested[1] != "other" {
		t.Errorf("nested list = %#v, want [%q other]", nested, redactedPlaceholder)
	}
	if got := ev.Context["header_sample"]; got != "Bearer "+token {
		t.Errorf("context[header_sample] = %#v, want the value unchanged", got)
	}
	if got := ev.Context["shaped_value"]; got != "aaa.bbb.ccc" {
		t.Errorf("context[shaped_value] = %#v, want the value unchanged", got)
	}
	if ev.Error.Chain[0] != redactedPlaceholder || ev.Error.Chain[1] != "dial tcp" {
		t.Errorf("error.chain = %#v, want [%q dial tcp]", ev.Error.Chain, redactedPlaceholder)
	}
	// error.message contains the token but is not equal to it, so the equality
	// rule does not fire. This is the documented consequence of matching keys
	// and equality only (Req 10.11).
	if ev.Error.Message != "post failed: "+token {
		t.Errorf("error.message = %q, want it unchanged", ev.Error.Message)
	}
	if ev.User.ID != redactedPlaceholder {
		t.Errorf("user.id = %q, want %q", ev.User.ID, redactedPlaceholder)
	}
	// message is outside the Redactor's scope (Req 10.2).
	if ev.Message != token {
		t.Errorf("message = %q, want it unmodified", ev.Message)
	}
}

func TestRedactorEmptyTokenDoesNotRedactEmptyStrings(t *testing.T) {
	r := NewRedactor("")

	ev := &Event{
		Context: map[string]any{"note": "", "list": []any{""}},
		User:    &UserInfo{ID: ""},
	}

	r.Apply(ev)

	if got := ev.Context["note"]; got != "" {
		t.Errorf("context[note] = %#v, want the empty string", got)
	}
	if got := ev.Context["list"].([]any)[0]; got != "" {
		t.Errorf("context[list][0] = %#v, want the empty string", got)
	}
	if ev.User.ID != "" {
		t.Errorf("user.id = %q, want the empty string", ev.User.ID)
	}
}

func TestRedactorLeavesUnscopedTopLevelFieldsAlone(t *testing.T) {
	r := NewRedactor("tok")

	ev := &Event{
		DT:          "2024-01-01T00:00:00.000Z",
		Level:       "ERROR",
		Message:     "boom",
		Service:     "fundis-backend",
		Environment: "production",
		Source:      &SourceInfo{File: "token.go", Line: 12, Function: "auth.SecretPath"},
	}
	before := *ev
	beforeSource := *ev.Source

	r.Apply(ev)

	if ev.DT != before.DT || ev.Level != before.Level || ev.Message != before.Message ||
		ev.Service != before.Service || ev.Environment != before.Environment || ev.Truncated {
		t.Errorf("top-level fields changed: %#v, want %#v", *ev, before)
	}
	if *ev.Source != beforeSource {
		t.Errorf("source = %#v, want %#v; source is outside the Redactor's scope (Req 10.2)", *ev.Source, beforeSource)
	}
}

// nestedContext returns a context map nesting keys k1..kN, whose leaf value
// therefore sits at rootDepth keys+1: context is at rootDepth 1, so the value
// of k1 is at 2, the value of k2 at 3, and so on.
func nestedContext(keys int) map[string]any {
	leaf := any("leaf")
	for i := keys; i >= 1; i-- {
		leaf = map[string]any{"k" + string(rune('0'+i%10)): leaf}
	}
	return leaf.(map[string]any)
}

func TestRedactorDepthCap(t *testing.T) {
	r := NewRedactor("")

	// Deepest retained value: rootDepth 10.
	retained := &Event{Context: nestedContext(int(maxRedactDepth) - 1)}
	r.Apply(retained)
	if retained.Truncated {
		t.Errorf("truncated = true for a value at rootDepth %d, want false", maxRedactDepth)
	}

	// One level deeper: rootDepth 11 is replaced and descent stops (Req 10.10).
	truncated := &Event{Context: nestedContext(int(maxRedactDepth))}
	r.Apply(truncated)
	if !truncated.Truncated {
		t.Errorf("truncated = false for a value at rootDepth %d, want true", maxRedactDepth+1)
	}
	if !containsPlaceholder(truncated.Context, truncatedPlaceholder) {
		t.Errorf("context = %#v, want a %q placeholder", truncated.Context, truncatedPlaceholder)
	}
}

func TestRedactorTerminatesOnCyclicEvent(t *testing.T) {
	cycle := map[string]any{"name": "loop"}
	cycle["self"] = cycle

	ev := &Event{Context: map[string]any{"cycle": cycle}}

	// The depth cap alone bounds this walk; no visited set is involved.
	NewRedactor("").Apply(ev)

	if !ev.Truncated {
		t.Error("truncated = false for a cyclic event, want true")
	}
	if !containsPlaceholder(ev.Context, truncatedPlaceholder) {
		t.Errorf("context = %#v, want a %q placeholder", ev.Context, truncatedPlaceholder)
	}
}

func TestRedactorApplyIsIdempotent(t *testing.T) {
	const token = "st_live_x"
	build := func() *Event {
		return &Event{
			Message: "boom",
			Context: map[string]any{
				"api_key": "k",
				"nested":  map[string]any{"cookie": []any{1, 2}, "ok": token},
				"deep":    nestedContext(int(maxRedactDepth)),
			},
			Error: &ErrorInfo{Message: token, Type: "*errors.errorString"},
			User:  &UserInfo{ID: "u-1"},
		}
	}

	once := build()
	NewRedactor(token).Apply(once)

	twice := build()
	NewRedactor(token).Apply(twice)
	NewRedactor(token).Apply(twice)

	if !reflect.DeepEqual(once, twice) {
		t.Errorf("Apply(Apply(ev)) = %#v, want %#v", twice, once)
	}
}

// containsPlaceholder reports whether want appears as a value anywhere in v.
func containsPlaceholder(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return t == want
	case map[string]any:
		for _, child := range t {
			if containsPlaceholder(child, want) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if containsPlaceholder(child, want) {
				return true
			}
		}
	}
	return false
}
