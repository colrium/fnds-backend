package logging

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Focused example-based coverage of the 64 KiB size cap. The universal
// statement — every event fits the cap and every privileged field survives it
// (Property 8) — is covered by the property test in task 2.9; these cases pin
// the specific behaviours that property is easy to satisfy vacuously.

// baseEvent returns a small, well-formed event with every privileged field
// populated, so a test can assert those fields are untouched after truncation.
func baseEvent() *Event {
	return &Event{
		DT:          "2024-05-01T10:20:30.123Z",
		Level:       "ERROR",
		Message:     "payment capture failed",
		Service:     "fundis-backend",
		Environment: "production",
		Error: &ErrorInfo{
			Message: "capture rejected",
			Type:    "*payments.CaptureError",
		},
		Source: &SourceInfo{
			File:     "capture.go",
			Line:     412,
			Function: "backend/payments.(*Service).Capture",
		},
	}
}

func mustMarshalLen(t *testing.T, ev *Event) int {
	t.Helper()
	encoded, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal(event): %v", err)
	}
	return len(encoded)
}

func TestCapEventSizeLeavesSmallEventUntouchedAndCachesLength(t *testing.T) {
	ev := baseEvent()
	ev.Context = map[string]any{"job_id": "job-42", "attempt": int64(3)}

	before := mustMarshalLen(t, ev)
	message, contextValue := ev.Message, ev.Context["job_id"]

	capEventSize(ev)

	if ev.Truncated {
		t.Error("truncated = true for an event inside the cap, want false (Req 3.14)")
	}
	if ev.Message != message {
		t.Errorf("message rewritten: got %q, want %q", ev.Message, message)
	}
	if ev.Context["job_id"] != contextValue {
		t.Errorf("context value rewritten: got %v, want %v", ev.Context["job_id"], contextValue)
	}
	if ev.SerializedLen() != before {
		t.Errorf("cached serialized length = %d, want %d", ev.SerializedLen(), before)
	}
}

func TestCapEventSizeTruncatesOversizedMessage(t *testing.T) {
	ev := baseEvent()
	// Multi-byte runes throughout, so a naive byte cut would split one and the
	// result would not be valid UTF-8 (Req 3.13).
	ev.Message = strings.Repeat("héllo wörld ", 20000)

	if got := mustMarshalLen(t, ev); got <= maxEventBytes {
		t.Fatalf("test setup: event is %d bytes, want more than %d", got, maxEventBytes)
	}

	capEventSize(ev)

	after := mustMarshalLen(t, ev)
	if after > maxEventBytes {
		t.Errorf("serialized event is %d bytes after capping, want at most %d", after, maxEventBytes)
	}
	if !ev.Truncated {
		t.Error("truncated = false after truncation, want true (Req 3.12)")
	}
	if !strings.HasSuffix(ev.Message, truncationIndicator) {
		t.Errorf("message does not end with the truncation indicator: %q", tail(ev.Message))
	}
	if !utf8.ValidString(ev.Message) {
		t.Error("truncated message is not valid UTF-8 (Req 3.13)")
	}
	if ev.SerializedLen() != after {
		t.Errorf("cached serialized length = %d, want %d", ev.SerializedLen(), after)
	}
}

func TestCapEventSizeTruncatesContextStringsLargestFirst(t *testing.T) {
	ev := baseEvent()
	// One dominant string plus a small one. Cutting the largest alone reclaims
	// the entire overage, so the small string and the message must survive
	// whole (Req 3.12: descending order of byte length).
	ev.Context = map[string]any{
		"response_body": strings.Repeat("A", 200000),
		"nested": map[string]any{
			"note": "short but present",
		},
	}
	message := ev.Message

	capEventSize(ev)

	after := mustMarshalLen(t, ev)
	if after > maxEventBytes {
		t.Errorf("serialized event is %d bytes after capping, want at most %d", after, maxEventBytes)
	}
	if !ev.Truncated {
		t.Error("truncated = false after truncation, want true")
	}

	body, ok := ev.Context["response_body"].(string)
	if !ok {
		t.Fatalf("context.response_body is %T, want string", ev.Context["response_body"])
	}
	if !strings.HasSuffix(body, truncationIndicator) {
		t.Errorf("largest context string was not cut: ends %q", tail(body))
	}
	if strings.Count(body, "A") == 0 {
		t.Error("largest context string lost all of its content, want a partial value")
	}

	nested, ok := ev.Context["nested"].(map[string]any)
	if !ok {
		t.Fatalf("context.nested is %T, want map[string]any", ev.Context["nested"])
	}
	if nested["note"] != "short but present" {
		t.Errorf("small nested string was truncated: %v", nested["note"])
	}
	if ev.Message != message {
		t.Errorf("message was truncated although context strings sufficed: %q", tail(ev.Message))
	}
}

func TestCapEventSizePreservesPrivilegedFields(t *testing.T) {
	ev := baseEvent()
	ev.Message = strings.Repeat("m", 100000)
	ev.Context = map[string]any{"payload": strings.Repeat("p", 500000)}

	want := baseEvent() // same privileged values, never truncated

	capEventSize(ev)

	if ev.DT != want.DT {
		t.Errorf("dt = %q, want %q (Req 3.17)", ev.DT, want.DT)
	}
	if ev.Level != want.Level {
		t.Errorf("level = %q, want %q (Req 3.17)", ev.Level, want.Level)
	}
	if ev.Service != want.Service {
		t.Errorf("service = %q, want %q (Req 3.17)", ev.Service, want.Service)
	}
	if ev.Environment != want.Environment {
		t.Errorf("environment = %q, want %q (Req 3.17)", ev.Environment, want.Environment)
	}
	if ev.Error.Type != want.Error.Type {
		t.Errorf("error.type = %q, want %q (Req 3.17)", ev.Error.Type, want.Error.Type)
	}
	if *ev.Source != *want.Source {
		t.Errorf("source = %+v, want %+v (Req 3.17)", *ev.Source, *want.Source)
	}
}

func TestCapEventSizeTerminatesOnMultiMegabyteMessage(t *testing.T) {
	ev := baseEvent()
	ev.Message = strings.Repeat("x", 4<<20) // 4 MiB, no context at all

	capEventSize(ev)

	after := mustMarshalLen(t, ev)
	if after > maxEventBytes {
		t.Errorf("serialized event is %d bytes after capping, want at most %d", after, maxEventBytes)
	}
	if ev.SerializedLen() > maxEventBytes {
		t.Errorf("cached serialized length = %d, want at most %d", ev.SerializedLen(), maxEventBytes)
	}
	if !ev.Truncated {
		t.Error("truncated = false after truncation, want true")
	}
}

// A context holding bulk that is not a string cannot be shortened by Req 3.12's
// two stages, so the documented last resort has to reclaim it rather than loop.
func TestCapEventSizeDropsContextEntriesAsLastResort(t *testing.T) {
	numbers := make([]any, 40000)
	for i := range numbers {
		numbers[i] = int64(i)
	}

	ev := baseEvent()
	ev.Context = map[string]any{"samples": numbers, "job_id": "job-42"}

	capEventSize(ev)

	after := mustMarshalLen(t, ev)
	if after > maxEventBytes {
		t.Errorf("serialized event is %d bytes after capping, want at most %d", after, maxEventBytes)
	}
	if _, present := ev.Context["samples"]; present {
		t.Error("oversized non-string context entry survived, want it dropped")
	}
	if ev.Context["job_id"] != "job-42" {
		t.Errorf("small context entry was dropped unnecessarily: %v", ev.Context["job_id"])
	}
	if !ev.Truncated {
		t.Error("truncated = false after dropping a context entry, want true")
	}
}

func TestCutStringCutsOnRuneBoundary(t *testing.T) {
	// Every rune is 2 bytes, so a cut that ignored boundaries would land inside
	// one for an odd reduce value.
	s := strings.Repeat("é", 50)

	for _, reduce := range []int{1, 3, 7, 25, 99, 1000} {
		got, ok := cutString(s, reduce)
		if !ok {
			t.Fatalf("cutString(reduce=%d) reported no progress for a %d-byte value", reduce, len(s))
		}
		if len(got) >= len(s) {
			t.Errorf("cutString(reduce=%d) returned %d bytes, want fewer than %d", reduce, len(got), len(s))
		}
		if !utf8.ValidString(got) {
			t.Errorf("cutString(reduce=%d) produced invalid UTF-8", reduce)
		}
		if !strings.HasSuffix(got, truncationIndicator) {
			t.Errorf("cutString(reduce=%d) omitted the truncation indicator", reduce)
		}
		kept := strings.TrimSuffix(got, truncationIndicator)
		if strings.Count(kept, "é")*2 != len(kept) {
			t.Errorf("cutString(reduce=%d) left a partial rune: %q", reduce, kept)
		}
	}
}

func TestCutStringReportsNoProgressForShortValues(t *testing.T) {
	for _, s := range []string{"", "short", truncationIndicator} {
		if got, ok := cutString(s, 1000); ok {
			t.Errorf("cutString(%q) = %q, true; want no progress reported", s, got)
		}
	}
}

// tail returns the last few bytes of s for use in failure messages, so a
// multi-megabyte value cannot flood the test output.
func tail(s string) string {
	const window = 32
	if len(s) <= window {
		return s
	}
	return "..." + s[len(s)-window:]
}
