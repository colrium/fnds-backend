package logging

// Log_Event wire representation. This file defines the shape only: it holds no
// conversion logic, reads no clock, and performs no I/O, so the payload
// contract can be read and tested in one place.
//
// The types deliberately carry no MarshalJSON method. Plain struct tags with
// encoding/json are enough for the required field set, and encoding/json sorts
// map keys, which makes the serialized bytes of the `context` object
// deterministic without any ordering work here.

// Event is the JSON object transmitted to Better Stack for one Log_Record.
//
// Field policy, straight from the requirements:
//
//   - dt, level, message, service, environment, and truncated are always
//     present at the top level (Req 3.1). None of them carries omitempty.
//   - truncated in particular must be emitted as an explicit false for an
//     event that underwent no truncation (Req 3.14), so it cannot carry
//     omitempty: encoding/json omits the false zero value.
//   - error, source, request, user, and context are optional objects. Each is
//     a pointer or a map tagged omitempty, so a nil value is absent from the
//     output rather than emitted as null (Req 3.16, 4.7, 4.10).
type Event struct {
	DT          string         `json:"dt"`
	Level       string         `json:"level"`
	Message     string         `json:"message"`
	Service     string         `json:"service"`
	Environment string         `json:"environment"`
	Truncated   bool           `json:"truncated"`
	Error       *ErrorInfo     `json:"error,omitempty"`
	Source      *SourceInfo    `json:"source,omitempty"`
	Request     *RequestInfo   `json:"request,omitempty"`
	User        *UserInfo      `json:"user,omitempty"`
	Context     map[string]any `json:"context,omitempty"`

	// serializedLen caches the byte length of this event's JSON encoding,
	// computed once at conversion time. The delivery goroutine's 1 MiB body cap
	// check reads it so that the check is O(1) per event instead of
	// re-marshalling the whole pending batch on every arrival (Req 5.14).
	//
	// It is unexported, so encoding/json ignores it and no internal bookkeeping
	// value can ever appear in a transmitted payload.
	serializedLen int
}

// ErrorInfo is the structured error object lifted out of the first
// error-valued attribute of a Log_Record (Req 3.6).
//
// Chain carries the successive unwraps of that error, outermost first, and is
// omitted entirely for an error that wraps nothing (Req 3.7).
type ErrorInfo struct {
	Message string   `json:"message"`
	Type    string   `json:"type"`
	Chain   []string `json:"chain,omitempty"`
}

// SourceInfo is the call site of a Log_Record (Req 3.8). File is the base file
// name only; Line is emitted as a JSON number. The whole object is omitted for
// a Log_Record that carries no call-site information (Req 3.16).
type SourceInfo struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
}

// RequestInfo correlates an Event with one inbound HTTP request (Req 4.5).
// Path is the registered route template, with no path parameter value
// substituted (Req 4.8).
type RequestInfo struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// UserInfo carries the authenticated user identifier byte-for-byte (Req 4.6).
type UserInfo struct {
	ID string `json:"id"`
}

// SerializedLen returns the cached byte length of this event's JSON encoding,
// or 0 when no length has been recorded yet.
//
// A zero result means "unknown", not "empty": no JSON encoding of an Event is
// shorter than the six required top-level fields. Callers that must not
// under-count treat 0 as a cache miss and measure the event themselves.
func (e *Event) SerializedLen() int {
	if e == nil {
		return 0
	}
	return e.serializedLen
}

// SetSerializedLen records the byte length of this event's JSON encoding. It is
// called once per event after conversion, and again after any truncation pass
// that changes the encoding, so the cached value never under-reports the bytes
// the event will contribute to a Delivery_Batch body.
//
// A negative length can only come from a caller bug; it is stored as 0, which
// reads back as a cache miss rather than as a size that would let the batch cap
// be exceeded.
func (e *Event) SetSerializedLen(n int) {
	if e == nil {
		return
	}
	if n < 0 {
		n = 0
	}
	e.serializedLen = n
}
