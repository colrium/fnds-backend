package logging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// testConvertConfig is the config every test here converts with, so a test that
// cares about service or environment overrides one field rather than restating
// both.
var testConvertConfig = convertConfig{ServiceName: "fundis-backend", Environment: "production"}

// newTestRecord builds a Log_Record with a fixed timestamp and no call site, so
// dt is deterministic and `source` is omitted unless a test asks for it.
func newTestRecord(level slog.Level, message string, args ...any) slog.Record {
	r := slog.NewRecord(
		time.Date(2025, 6, 14, 9, 21, 44, 317_000_000, time.UTC),
		level,
		message,
		0,
	)
	r.Add(args...)
	return r
}

// convertForTest converts with no handler-bound frames and no open groups, the
// shape produced by a plain slog.Error call site.
func convertForTest(t *testing.T, r slog.Record) *Event {
	t.Helper()
	return convertRecord(context.Background(), r, testConvertConfig, nil, nil)
}

// contextOf decodes the `context` object of the marshalled event, so assertions
// run against the JSON that would go on the wire rather than against the
// in-memory tree.
func contextOf(t *testing.T, ev *Event) map[string]any {
	t.Helper()

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal(Event) returned error: %v", err)
	}
	var decoded struct {
		Context map[string]any `json:"context"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("marshalled Event does not decode: %v (%s)", err, raw)
	}
	return decoded.Context
}

func TestConvertRecordTopLevelFields(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "failed to record referral"))

	// Req 3.2: RFC 3339, UTC, exactly three fractional digits, literal Z.
	if got, want := ev.DT, "2025-06-14T09:21:44.317Z"; got != want {
		t.Errorf("DT = %q, want %q", got, want)
	}
	if got, want := ev.Level, "ERROR"; got != want {
		t.Errorf("Level = %q, want %q", got, want) // Req 3.3
	}
	if got, want := ev.Message, "failed to record referral"; got != want {
		t.Errorf("Message = %q, want %q", got, want) // Req 3.4
	}
	if got, want := ev.Service, "fundis-backend"; got != want {
		t.Errorf("Service = %q, want %q", got, want) // Req 3.5
	}
	if got, want := ev.Environment, "production"; got != want {
		t.Errorf("Environment = %q, want %q", got, want) // Req 3.15
	}
	if ev.Truncated {
		t.Error("Truncated = true for a record that needed no truncation") // Req 3.14
	}
	if ev.Context != nil {
		t.Errorf("Context = %v for a record with no attrs, want omitted", ev.Context)
	}
}

func TestConvertRecordDTIsUTCWithThreeFractionalDigits(t *testing.T) {
	// A non-UTC zone with sub-millisecond precision: the offset must be
	// normalized away and the fraction cut to three digits, not rounded up into
	// a fourth (Req 3.2).
	zone := time.FixedZone("EAT", 3*60*60)
	r := slog.NewRecord(time.Date(2025, 1, 2, 3, 4, 5, 678_999_999, zone), slog.LevelError, "m", 0)

	ev := convertForTest(t, r)
	if got, want := ev.DT, "2025-01-02T00:04:05.678Z"; got != want {
		t.Errorf("DT = %q, want %q", got, want)
	}
}

func TestConvertRecordLevelAboveError(t *testing.T) {
	// Req 2.7, 3.3: a level above a named level is reported as the named level
	// plus its positive offset.
	ev := convertForTest(t, newTestRecord(slog.LevelError+2, "custom severity"))
	if got, want := ev.Level, "ERROR+2"; got != want {
		t.Errorf("Level = %q, want %q", got, want)
	}
}

func TestConvertRecordEmptyMessageAndUnknownFields(t *testing.T) {
	ev := convertRecord(context.Background(), newTestRecord(slog.LevelError, ""), convertConfig{}, nil, nil)

	if got := ev.Message; got != "" {
		t.Errorf("Message = %q, want the empty string", got) // Req 3.4
	}
	if got, want := ev.Service, unknownFieldValue; got != want {
		t.Errorf("Service = %q, want %q", got, want) // Req 3.5
	}
	if got, want := ev.Environment, unknownFieldValue; got != want {
		t.Errorf("Environment = %q, want %q", got, want) // Req 3.15
	}
}

func TestConvertRecordCapsServiceAndEnvironmentAt64Characters(t *testing.T) {
	// Multi-byte runes: the cut is 64 characters, not 64 bytes, and must not
	// leave a broken sequence behind.
	long := strings.Repeat("é", 100)
	cfg := convertConfig{ServiceName: long, Environment: long}

	ev := convertRecord(context.Background(), newTestRecord(slog.LevelError, "m"), cfg, nil, nil)

	for field, got := range map[string]string{"Service": ev.Service, "Environment": ev.Environment} {
		if want := strings.Repeat("é", maxTopLevelFieldChars); got != want {
			t.Errorf("%s = %q (%d bytes), want the first %d characters", field, got, len(got), maxTopLevelFieldChars)
		}
	}
}

func TestConvertRecordLiftsFirstErrorAttr(t *testing.T) {
	inner := errors.New("(NotWritablePrimary) not primary")
	wrapped := fmt.Errorf("creating referral: %w", inner)
	second := errors.New("secondary failure")

	ev := convertForTest(t, newTestRecord(slog.LevelError, "failed to record referral",
		"referred_user_id", "6653f1c2a8de40b1c0a91f77",
		"error", wrapped,
		"cleanup_error", second,
	))

	if ev.Error == nil {
		t.Fatal("Error object missing for a record carrying an error attr") // Req 3.6
	}
	if got, want := ev.Error.Message, "creating referral: (NotWritablePrimary) not primary"; got != want {
		t.Errorf("Error.Message = %q, want %q", got, want)
	}
	if got, want := ev.Error.Type, "*fmt.wrapError"; got != want {
		t.Errorf("Error.Type = %q, want %q", got, want)
	}
	// Req 3.7: outermost first, one entry per unwrap step.
	want := []string{"creating referral: (NotWritablePrimary) not primary", "(NotWritablePrimary) not primary"}
	if len(ev.Error.Chain) != len(want) {
		t.Fatalf("Error.Chain = %v, want %v", ev.Error.Chain, want)
	}
	for i := range want {
		if ev.Error.Chain[i] != want[i] {
			t.Errorf("Error.Chain[%d] = %q, want %q", i, ev.Error.Chain[i], want[i])
		}
	}

	ctxObj := contextOf(t, ev)
	if _, present := ctxObj["error"]; present {
		t.Errorf("the lifted error attr is still under context: %v", ctxObj)
	}
	// Req 3.6: every remaining error-valued attr stays under context as text.
	if got, want := ctxObj["cleanup_error"], "secondary failure"; got != want {
		t.Errorf("context[cleanup_error] = %v, want %q", got, want)
	}
	if got, want := ctxObj["referred_user_id"], "6653f1c2a8de40b1c0a91f77"; got != want {
		t.Errorf("context[referred_user_id] = %v, want %q", got, want) // Req 3.9
	}
}

func TestConvertRecordOmitsChainForUnwrappedError(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "boom", "error", errors.New("boom")))

	if ev.Error == nil {
		t.Fatal("Error object missing")
	}
	if ev.Error.Chain != nil {
		t.Errorf("Error.Chain = %v, want omitted for an error that wraps nothing", ev.Error.Chain) // Req 3.7
	}
	if ev.Truncated {
		t.Error("Truncated = true for a single-link error chain")
	}
}

func TestConvertRecordCapsErrorChainAtTen(t *testing.T) {
	err := error(errors.New("level 0"))
	for i := 1; i <= 14; i++ {
		err = fmt.Errorf("level %d: %w", i, err)
	}

	ev := convertForTest(t, newTestRecord(slog.LevelError, "deep", "error", err))

	if ev.Error == nil {
		t.Fatal("Error object missing")
	}
	if got := len(ev.Error.Chain); got != maxErrorChainLen {
		t.Errorf("len(Error.Chain) = %d, want %d", got, maxErrorChainLen) // Req 3.7
	}
	if !ev.Truncated {
		t.Error("Truncated = false for a chain longer than 10 entries") // Req 3.7
	}
	if got, want := ev.Error.Chain[0], "level 14: level 13: level 12"; !strings.HasPrefix(got, want) {
		t.Errorf("Error.Chain[0] = %q, want the outermost error first", got)
	}
}

func TestConvertRecordTypedNilErrorIsNotLifted(t *testing.T) {
	// A nil pointer stored in an error interface is non-nil as an interface but
	// panics on Error(). It must convert as JSON null instead (Req 3.13), rather
	// than costing the whole record a conversion panic.
	var nilErr error = (*nilError)(nil)
	ev := convertForTest(t, newTestRecord(slog.LevelError, "m", "error", nilErr))

	if ev.Error != nil {
		t.Errorf("Error = %+v, want no error object for a typed nil", ev.Error)
	}
	ctxObj := contextOf(t, ev)
	value, present := ctxObj["error"]
	if !present {
		t.Fatalf("context = %v, want the typed nil attr present as null", ctxObj)
	}
	if value != nil {
		t.Errorf("context[error] = %#v, want JSON null", value)
	}
}

// nilError is only ever used as a nil pointer, so Error would panic if called.
type nilError struct{}

func (e *nilError) Error() string { return e.field() }

func (e *nilError) field() string { return "unreachable" }

func TestConvertRecordSourceFromPC(t *testing.T) {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:]) // this function's call site

	r := slog.NewRecord(time.Now(), slog.LevelError, "m", pcs[0])
	ev := convertForTest(t, r)

	if ev.Source == nil {
		t.Fatal("Source omitted for a record carrying a program counter") // Req 3.8
	}
	if got, want := ev.Source.File, "convert_test.go"; got != want {
		t.Errorf("Source.File = %q, want the base file name %q", got, want)
	}
	if ev.Source.Line == 0 {
		t.Error("Source.Line = 0, want the call site line number")
	}
	if !strings.Contains(ev.Source.Function, "TestConvertRecordSourceFromPC") {
		t.Errorf("Source.Function = %q, want the enclosing function", ev.Source.Function)
	}
}

func TestConvertRecordOmitsSourceWithoutPC(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "m"))
	if ev.Source != nil {
		t.Errorf("Source = %+v, want omitted when the record carries no call site", ev.Source) // Req 3.16
	}
}

func TestConvertRecordCorrelationFromContext(t *testing.T) {
	ctx := ContextWithUserID(
		ContextWithRequest(context.Background(), "rid-1", "POST", "/api/v1/register"),
		"6653f1c2a8de40b1c0a91f77",
	)

	ev := convertRecord(ctx, newTestRecord(slog.LevelError, "m"), testConvertConfig, nil, nil)

	if ev.Request == nil {
		t.Fatal("Request omitted although the context carries a request id") // Req 4.5
	}
	if *ev.Request != (RequestInfo{ID: "rid-1", Method: "POST", Path: "/api/v1/register"}) {
		t.Errorf("Request = %+v, want the context triple verbatim", *ev.Request)
	}
	if ev.User == nil || ev.User.ID != "6653f1c2a8de40b1c0a91f77" {
		t.Errorf("User = %+v, want the context user id byte-for-byte", ev.User) // Req 4.6
	}
}

func TestConvertRecordOmitsCorrelationWithoutContextValues(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "m"))

	if ev.Request != nil {
		t.Errorf("Request = %+v, want omitted for an uncorrelated record", ev.Request) // Req 4.7
	}
	if ev.User != nil {
		t.Errorf("User = %+v, want omitted when no user id is present", ev.User) // Req 4.10
	}
}

func TestConvertRecordReservedKeysGoUnderContext(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "real message",
		"dt", "not-a-timestamp",
		"level", "TRACE",
		"message", "shadow",
		"service", "impostor",
		"environment", "staging",
		"source", "nowhere",
		"context", "nested",
		"truncated", true,
	))

	// Req 3.10: every reserved-key attr lands under context and the top-level
	// field keeps the value conversion derived.
	if ev.DT == "not-a-timestamp" || ev.Level != "ERROR" || ev.Message != "real message" ||
		ev.Service != "fundis-backend" || ev.Environment != "production" || ev.Truncated {
		t.Errorf("reserved-key attrs overwrote a top-level field: %+v", ev)
	}
	if ev.Source != nil {
		t.Errorf("Source = %+v, want untouched by a `source` attr", ev.Source)
	}

	ctxObj := contextOf(t, ev)
	for key, want := range map[string]any{
		"dt": "not-a-timestamp", "level": "TRACE", "message": "shadow",
		"service": "impostor", "environment": "staging", "source": "nowhere",
		"context": "nested", "truncated": true,
	} {
		if got := ctxObj[key]; got != want {
			t.Errorf("context[%s] = %#v, want %#v", key, got, want)
		}
	}
}

func TestConvertRecordGroupHandling(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "m",
		slog.Group("db", slog.String("collection", "referrals"), slog.Int("attempt", 2)),
		slog.Group(""), // an empty unnamed group contributes nothing
		slog.Group("empty"),
		slog.Group("", slog.String("inlined", "yes")), // an unnamed group is inlined
	))

	ctxObj := contextOf(t, ev)

	db, ok := ctxObj["db"].(map[string]any)
	if !ok {
		t.Fatalf("context[db] = %#v, want a nested object", ctxObj["db"]) // Req 3.9
	}
	if got, want := db["collection"], "referrals"; got != want {
		t.Errorf("context.db.collection = %#v, want %q", got, want)
	}
	if got, want := db["attempt"], float64(2); got != want {
		t.Errorf("context.db.attempt = %#v, want %v", got, want)
	}
	if _, present := ctxObj["empty"]; present {
		t.Errorf("context = %v, want an empty group dropped", ctxObj)
	}
	if got, want := ctxObj["inlined"], "yes"; got != want {
		t.Errorf("context[inlined] = %#v, want %q at the top level of context", got, want)
	}
	if ev.Truncated {
		t.Error("Truncated = true for a record well inside the depth cap")
	}
}

func TestConvertRecordLaterDuplicateKeyWins(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "m",
		"attempt", 1,
		"attempt", 2,
		slog.Group("db", slog.Int("attempt", 1), slog.Int("attempt", 3)),
	))

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal(Event) returned error: %v", err)
	}
	// Req 3.11: the last value wins and the key is emitted exactly once.
	if got := strings.Count(string(raw), `"attempt"`); got != 2 {
		t.Errorf("`attempt` appears %d times in %s, want once per object level", got, raw)
	}

	ctxObj := contextOf(t, ev)
	if got, want := ctxObj["attempt"], float64(2); got != want {
		t.Errorf("context[attempt] = %#v, want %v", got, want)
	}
	db, _ := ctxObj["db"].(map[string]any)
	if got, want := db["attempt"], float64(3); got != want {
		t.Errorf("context.db.attempt = %#v, want %v", got, want)
	}
}

func TestConvertRecordFramesAndOpenGroups(t *testing.T) {
	// The shape produced by
	// slog.With("a", 1).WithGroup("g").With("b", 2) followed by a record
	// emitted while g is still open, plus a second frame that reopens g.
	frames := []attrFrame{
		{groups: nil, attrs: []slog.Attr{slog.Int("a", 1)}},
		{groups: []string{"g"}, attrs: []slog.Attr{slog.Int("b", 2)}},
		{groups: []string{"g"}, attrs: []slog.Attr{slog.Int("c", 3)}},
	}

	ev := convertRecord(
		context.Background(),
		newTestRecord(slog.LevelError, "m", "d", 4),
		testConvertConfig,
		frames,
		[]string{"g"},
	)

	ctxObj := contextOf(t, ev)
	if got, want := ctxObj["a"], float64(1); got != want {
		t.Errorf("context[a] = %#v, want %v: a was added before any group was open", got, want)
	}
	g, ok := ctxObj["g"].(map[string]any)
	if !ok {
		t.Fatalf("context[g] = %#v, want a nested object", ctxObj["g"])
	}
	// Reopening a group merges rather than replacing, so b, c, and the record's
	// own d all sit under g.
	for key, want := range map[string]float64{"b": 2, "c": 3, "d": 4} {
		if got := g[key]; got != any(want) {
			t.Errorf("context.g.%s = %#v, want %v", key, got, want)
		}
	}
}

func TestConvertRecordDepthCap(t *testing.T) {
	// Nest one attr under eight groups: it lands at ctxLevel 9, one past the cap,
	// so it is omitted and truncated latches (Req 3.9). Each group on the way
	// down also carries a shallow sibling, so the walk can show which levels
	// survive; a group left empty by the cap is dropped along with its key, and
	// that removal cascades up through every ancestor it emptied.
	deep := []slog.Attr{slog.String("leaf", "value")}
	for i := 8; i >= 1; i-- {
		deep = []slog.Attr{{Key: fmt.Sprintf("g%d", i), Value: slog.GroupValue(
			slog.String("sibling", fmt.Sprintf("level %d", i)),
			deep[0],
		)}}
	}
	r := newTestRecord(slog.LevelError, "m")
	r.AddAttrs(deep...)

	ev := convertForTest(t, r)

	if !ev.Truncated {
		t.Error("Truncated = false although an attr was dropped by the depth cap")
	}

	// g1..g7 are objects, and each holds its own sibling. The deepest retained
	// value is g7's sibling, at ctxLevel 8. g8 is at ctxLevel 8 itself, but
	// everything it could hold sits at ctxLevel 9, so it is dropped as an empty
	// group rather than emitted as an empty object.
	level := contextOf(t, ev)
	for i := 1; i <= 7; i++ {
		next, ok := level[fmt.Sprintf("g%d", i)].(map[string]any)
		if !ok {
			t.Fatalf("context%s is not an object: %#v", strings.Repeat(".g", i), level)
		}
		if got, want := next["sibling"], fmt.Sprintf("level %d", i); got != want {
			t.Errorf("context.g%d.sibling = %#v, want %q", i, got, want)
		}
		level = next
	}
	if _, present := level["g8"]; present {
		t.Errorf("g8 survived although the cap left it empty: %#v", level)
	}

	// A record whose deepest level is exactly at the cap keeps everything and
	// reports truncated: false.
	atCap := []slog.Attr{slog.String("leaf", "value")}
	for i := 7; i >= 1; i-- {
		atCap = []slog.Attr{slog.Attr{Key: fmt.Sprintf("g%d", i), Value: slog.GroupValue(atCap...)}}
	}
	rAtCap := newTestRecord(slog.LevelError, "m")
	rAtCap.AddAttrs(atCap...)

	evAtCap := convertForTest(t, rAtCap)
	if evAtCap.Truncated {
		t.Error("Truncated = true for an attr sitting exactly at the depth cap")
	}
	level = contextOf(t, evAtCap)
	for i := 1; i <= 7; i++ {
		next, ok := level[fmt.Sprintf("g%d", i)].(map[string]any)
		if !ok {
			t.Fatalf("context%s is not an object: %#v", strings.Repeat(".g", i), level)
		}
		level = next
	}
	if got, want := level["leaf"], "value"; got != want {
		t.Errorf("deepest retained value = %#v, want %q", got, want)
	}
}

// resolvedValue is a LogValuer whose replacement value is what must be emitted
// and, later, redacted (Req 10.8).
type resolvedValue struct{}

func (resolvedValue) LogValue() slog.Value {
	return slog.GroupValue(slog.String("api_key", "sk-live-secret"), slog.Int("retries", 3))
}

func TestConvertRecordResolvesLogValuer(t *testing.T) {
	ev := convertForTest(t, newTestRecord(slog.LevelError, "m", "credentials", resolvedValue{}))

	ctxObj := contextOf(t, ev)
	credentials, ok := ctxObj["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("context[credentials] = %#v, want the resolved group as an object", ctxObj["credentials"])
	}
	if got, want := credentials["api_key"], "sk-live-secret"; got != want {
		t.Errorf("context.credentials.api_key = %#v, want %q before redaction runs", got, want)
	}
	if got, want := credentials["retries"], float64(3); got != want {
		t.Errorf("context.credentials.retries = %#v, want %v", got, want)
	}

	// The resolved value is a plain map, which is what lets the Redactor reach
	// the sensitive key at every depth (Req 10.9).
	NewRedactor("").Apply(ev)
	if got, want := contextOf(t, ev)["credentials"].(map[string]any)["api_key"], redactedPlaceholder; got != want {
		t.Errorf("after redaction context.credentials.api_key = %#v, want %q", got, want)
	}
}

func TestConvertRecordValueKinds(t *testing.T) {
	type payload struct {
		Nested  map[string]string `json:"nested"`
		Numbers []int64           `json:"numbers"`
	}

	ev := convertForTest(t, newTestRecord(slog.LevelError, "m",
		"str", "text",
		"bool", true,
		"int", -7,
		"uint", uint64(1)<<63,
		"float", 1.5,
		"nan", math.NaN(),
		"duration", 1500*time.Millisecond,
		"time", time.Date(2025, 6, 14, 9, 21, 44, 317_000_000, time.UTC),
		"big", int64(1234567890123456789),
		"struct", payload{Nested: map[string]string{"api_key": "x"}, Numbers: []int64{1, 2}},
		"nil", nil,
	))

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal(Event) returned error: %v", err)
	}
	ctxObj := contextOf(t, ev)

	if got, want := ctxObj["str"], "text"; got != want {
		t.Errorf("context[str] = %#v, want %q", got, want)
	}
	if got := ctxObj["bool"]; got != true {
		t.Errorf("context[bool] = %#v, want true", got)
	}
	if got, want := ctxObj["int"], float64(-7); got != want {
		t.Errorf("context[int] = %#v, want %v", got, want)
	}
	if got, want := ctxObj["float"], 1.5; got != want {
		t.Errorf("context[float] = %#v, want %v", got, want)
	}
	// JSON has no NaN literal, so Req 3.13 asks for default Go formatting.
	if got, want := ctxObj["nan"], "NaN"; got != want {
		t.Errorf("context[nan] = %#v, want %q", got, want)
	}
	if got, want := ctxObj["duration"], "1.5s"; got != want {
		t.Errorf("context[duration] = %#v, want %q", got, want)
	}
	if got, want := ctxObj["time"], "2025-06-14T09:21:44.317Z"; got != want {
		t.Errorf("context[time] = %#v, want %q", got, want)
	}
	// A large integer must survive the normalization round trip exactly, which
	// is why numbers are decoded as json.Number rather than float64.
	if !strings.Contains(string(raw), "1234567890123456789") {
		t.Errorf("large int64 lost precision: %s", raw)
	}
	if !strings.Contains(string(raw), "9223372036854775808") {
		t.Errorf("uint64 above MaxInt64 not emitted exactly: %s", raw)
	}
	// A struct is normalized into plain maps and slices so redaction can reach
	// into it.
	structValue, ok := ctxObj["struct"].(map[string]any)
	if !ok {
		t.Fatalf("context[struct] = %#v, want an object", ctxObj["struct"])
	}
	nested, ok := structValue["nested"].(map[string]any)
	if !ok {
		t.Fatalf("context.struct.nested = %#v, want an object", structValue["nested"])
	}
	if got, want := nested["api_key"], "x"; got != want {
		t.Errorf("context.struct.nested.api_key = %#v, want %q", got, want)
	}
	if _, ok := structValue["numbers"].([]any); !ok {
		t.Errorf("context.struct.numbers = %#v, want an array", structValue["numbers"])
	}
	// Req 3.13: a nil attribute value is JSON null, present rather than dropped.
	value, present := ctxObj["nil"]
	if !present || value != nil {
		t.Errorf("context[nil] = %#v (present=%v), want JSON null", value, present)
	}
}

func TestConvertRecordHostileValuesProduceValidJSON(t *testing.T) {
	selfReferential := map[string]any{"name": "cycle"}
	selfReferential["self"] = selfReferential

	type withChannel struct {
		Ch chan int `json:"ch"`
	}
	type scalars struct {
		A int
		B string
	}

	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice

	ev := convertForTest(t, newTestRecord(slog.LevelError, "hostile \xff message",
		"invalid_utf8", "value \xff\xfe here",
		"invalid_utf8_key\xff", "keyed",
		"channel", make(chan int),
		"func", func() {},
		"complex", complex(1, 2),
		"struct_with_channel", withChannel{Ch: make(chan int)},
		"scalar_struct", scalars{A: 1, B: "two"},
		"self_referential_map", selfReferential,
		"self_referential_slice", cyclicSlice,
		"nil_pointer", (*scalars)(nil),
		"inf", math.Inf(-1),
	))

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal(Event) returned error: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("conversion produced invalid JSON: %s", raw)
	}
	if !utf8.Valid(raw) {
		t.Fatalf("conversion produced invalid UTF-8: %q", raw)
	}
	if strings.Contains(ev.Message, "\xff") {
		t.Errorf("Message = %q, want invalid bytes replaced", ev.Message)
	}

	ctxObj := contextOf(t, ev)
	if got := ctxObj["invalid_utf8"]; got != "value "+string(utf8.RuneError)+" here" {
		t.Errorf("context[invalid_utf8] = %#v, want the invalid run replaced", got)
	}
	if _, present := ctxObj["invalid_utf8_key"+string(utf8.RuneError)]; !present {
		t.Errorf("context = %v, want the sanitized key present", ctxObj)
	}
	// A scalar-only struct is representable, so it round-trips as an object.
	if _, ok := ctxObj["scalar_struct"].(map[string]any); !ok {
		t.Errorf("context[scalar_struct] = %#v, want an object", ctxObj["scalar_struct"])
	}
	// Values JSON cannot represent fall back to Go formatting.
	for _, key := range []string{"channel", "func", "complex", "struct_with_channel"} {
		if _, ok := ctxObj[key].(string); !ok {
			t.Errorf("context[%s] = %#v, want default Go string formatting", key, ctxObj[key])
		}
	}
	if got, want := ctxObj["inf"], "-Inf"; got != want {
		t.Errorf("context[inf] = %#v, want %q", got, want)
	}
	// A self-referential container is described rather than formatted: fmt does
	// not detect cycles in maps and slices, and a stack overflow is fatal.
	for _, key := range []string{"self_referential_map", "self_referential_slice"} {
		text, ok := ctxObj[key].(string)
		if !ok || !strings.HasPrefix(text, "<unrepresentable ") {
			t.Errorf("context[%s] = %#v, want an unrepresentable-value description", key, ctxObj[key])
		}
	}
	if value, present := ctxObj["nil_pointer"]; !present || value != nil {
		t.Errorf("context[nil_pointer] = %#v (present=%v), want JSON null", value, present)
	}
}

func TestConvertRecordSkipsEmptyAttrs(t *testing.T) {
	r := newTestRecord(slog.LevelError, "m")
	r.AddAttrs(slog.Attr{}, slog.String("kept", "yes"), slog.Attr{})

	ev := convertForTest(t, r)

	ctxObj := contextOf(t, ev)
	if len(ctxObj) != 1 || ctxObj["kept"] != "yes" {
		t.Errorf("context = %v, want only the non-empty attr", ctxObj)
	}
}
