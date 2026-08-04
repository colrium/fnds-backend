package logging

import (
	"encoding/json"
	"strings"
	"testing"
)

// marshalEvent marshals ev and decodes the result into a generic map, so tests
// can assert on presence and absence of keys rather than on byte equality.
func marshalEvent(t *testing.T, ev *Event) (map[string]json.RawMessage, []byte) {
	t.Helper()

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal(Event) returned error: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("marshalled Event does not decode as a JSON object: %v (%s)", err, raw)
	}
	return fields, raw
}

func TestEventAlwaysEmitsRequiredTopLevelFields(t *testing.T) {
	// A zero-ish Event: nothing set, which is the worst case for omitempty.
	fields, raw := marshalEvent(t, &Event{})

	// Req 3.1: dt, level, message, service, environment, and truncated are
	// present on every Log_Event.
	for _, key := range []string{"dt", "level", "message", "service", "environment", "truncated"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("required top-level field %q missing from %s", key, raw)
		}
	}
}

func TestEventEmitsTruncatedFalseExplicitly(t *testing.T) {
	fields, raw := marshalEvent(t, &Event{})

	// Req 3.14: an event that underwent zero truncation reports truncated as an
	// explicit false. omitempty on this field would silently drop it.
	value, ok := fields["truncated"]
	if !ok {
		t.Fatalf("truncated omitted from %s; it must be emitted as explicit false", raw)
	}
	if got := string(value); got != "false" {
		t.Errorf("truncated = %s, want false", got)
	}
}

func TestEventOmitsOptionalObjectsWhenNil(t *testing.T) {
	fields, raw := marshalEvent(t, &Event{})

	// Req 3.16, 4.7, 4.10: an unset optional object is absent, not null.
	for _, key := range []string{"error", "source", "request", "user", "context"} {
		if _, ok := fields[key]; ok {
			t.Errorf("optional field %q present for a nil value; want omitted. got %s", key, raw)
		}
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("marshalled Event contains a null value: %s", raw)
	}
}

func TestEventEmitsOptionalObjectsWhenSet(t *testing.T) {
	ev := &Event{
		DT:          "2025-06-14T09:21:44.317Z",
		Level:       "ERROR",
		Message:     "failed to record referral",
		Service:     "fundis-backend",
		Environment: "production",
		Error: &ErrorInfo{
			Message: "creating referral: not primary",
			Type:    "*fmt.wrapError",
			Chain:   []string{"creating referral: not primary", "not primary"},
		},
		Source:  &SourceInfo{File: "routes.go", Line: 199, Function: "backend/auth.HandleUserRegistration"},
		Request: &RequestInfo{ID: "rid-1", Method: "POST", Path: "/api/v1/register"},
		User:    &UserInfo{ID: "6653f1c2a8de40b1c0a91f77"},
		Context: map[string]any{"referred_user_id": "6653f1c2a8de40b1c0a91f77"},
	}

	fields, raw := marshalEvent(t, ev)
	for _, key := range []string{"error", "source", "request", "user", "context"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("field %q missing although it was set: %s", key, raw)
		}
	}

	// source.line is a JSON number, not a string (Req 3.8).
	if got := string(fields["source"]); !strings.Contains(got, `"line":199`) {
		t.Errorf("source = %s, want line emitted as the number 199", got)
	}

	// An error that wraps nothing omits chain entirely (Req 3.7).
	noChain, _ := marshalEvent(t, &Event{Error: &ErrorInfo{Message: "boom", Type: "*errors.errorString"}})
	if got := string(noChain["error"]); strings.Contains(got, "chain") {
		t.Errorf("error = %s, want chain omitted when empty", got)
	}
}

func TestEventSerializedLenNeverAppearsInJSON(t *testing.T) {
	ev := &Event{Message: "hi"}
	ev.SetSerializedLen(4242)

	_, raw := marshalEvent(t, ev)
	if strings.Contains(string(raw), "4242") || strings.Contains(strings.ToLower(string(raw)), "serializedlen") {
		t.Errorf("cached serialized length leaked into JSON output: %s", raw)
	}
}

func TestEventSerializedLenRoundTrips(t *testing.T) {
	ev := &Event{}
	if got := ev.SerializedLen(); got != 0 {
		t.Errorf("SerializedLen() on a fresh Event = %d, want 0", got)
	}

	_, raw := marshalEvent(t, ev)
	ev.SetSerializedLen(len(raw))
	if got, want := ev.SerializedLen(), len(raw); got != want {
		t.Errorf("SerializedLen() = %d, want %d", got, want)
	}

	// A negative length is a caller bug; it reads back as a cache miss rather
	// than as a value that would let the 1 MiB batch cap be exceeded.
	ev.SetSerializedLen(-1)
	if got := ev.SerializedLen(); got != 0 {
		t.Errorf("SerializedLen() after SetSerializedLen(-1) = %d, want 0", got)
	}
}

func TestNilEventAccessorsAreSafe(t *testing.T) {
	var ev *Event
	if got := ev.SerializedLen(); got != 0 {
		t.Errorf("SerializedLen() on a nil Event = %d, want 0", got)
	}
	ev.SetSerializedLen(10) // must not panic
}
