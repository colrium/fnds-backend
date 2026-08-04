package logging

// Tests for the Self_Diagnostic path.
//
// The single most important assertion in this file is a negative one: writing a
// diagnostic — at any level, including ERROR, and including one that describes a
// drop — adds zero Log_Events to the Event_Queue and records zero admissions,
// even while slog.Default() is the fan-out handler that feeds the Shipper
// (Req 8.1, 8.2, 11.8). That is the no-feedback-loop guarantee, and the only way
// to see it from outside is to wire the loop up and watch it not close.
//
// The rest pins the contents Req 11.9 fixes for the startup summary, the level
// fidelity of the deferred validation diagnostics, the 60 s drop window on the
// injected clock (Req 6.4, 6.8), and the stderr default of Req 12.8.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// testDiagToken must not appear in any diagnostic (Req 7.13, 10.4). It is
	// distinctive so a substring search over the whole output is conclusive.
	testDiagToken = "diag-test-source-token-ZZTOP"
	// testDiagQuery is the query string Req 10.5 and Req 11.9 exclude.
	testDiagQuery = "source_token=QUERYSECRET"
	// testDiagHost is the host Req 11.9 requires in the startup summary.
	testDiagHost = "in.diag.example.test"
	// testDiagURL carries userinfo, a path, and a query string, so a helper that
	// renders too much of it fails loudly.
	testDiagURL = "https://user:pw@" + testDiagHost + "/v1/logs?" + testDiagQuery
)

// newDiagShipper builds an enabled Shipper whose diagnostics land in the returned
// buffer and whose clock is a fake one, resolved through ResolveConfig so the
// defaulting and clamping are production's (Req 12.2, 12.8).
//
// It is deliberately not started: nothing drains the queue, so a queue-full drop
// is reachable by emitting cap+1 records.
func newDiagShipper(t *testing.T, queueSize int) (*Shipper, *bytes.Buffer, *fakeClock) {
	t.Helper()

	cfg, diags := ResolveConfig(RawConfig{
		SourceToken: testDiagToken,
		IngestURL:   testDiagURL,
		QueueSize:   strconv.Itoa(queueSize),
	})
	if !cfg.Enabled {
		t.Fatalf("ResolveConfig produced a disabled Config for a valid token and URL: %+v", diags)
	}

	diagnostics := new(bytes.Buffer)
	clock := newFakeClock()
	cfg.DiagWriter = diagnostics
	cfg.Clock = clock

	return NewShipper(cfg), diagnostics, clock
}

// diagRecord returns the single diagnostic in the buffer, failing unless there is
// exactly one.
func diagRecord(t *testing.T, diagnostics *bytes.Buffer) map[string]any {
	t.Helper()
	records := diagRecords(t, diagnostics)
	if len(records) != 1 {
		t.Fatalf("wrote %d Self_Diagnostics, want exactly 1: %q", len(records), diagnostics.String())
	}
	return records[0]
}

// diagNumber reads a JSON number field, which json.Unmarshal always decodes into
// a float64.
func diagNumber(t *testing.T, record map[string]any, key string) float64 {
	t.Helper()
	value, ok := record[key].(float64)
	if !ok {
		t.Fatalf("Self_Diagnostic field %q = %#v, want a JSON number", key, record[key])
	}
	return value
}

func TestDiagnosticsCarryTheMarkerAndGoToTheInjectedWriter(t *testing.T) {
	// Req 8.1: every Self_Diagnostic goes through a logger bound directly to a
	// writer and carries the marker attribute that identifies it as shipper
	// self-diagnostic output. Req 12.8: that writer is the one supplied at
	// construction.
	s, diagnostics, _ := newDiagShipper(t, 4)

	s.diagf(slog.LevelWarn, "diagnostic under test", slog.String("detail", "value"))

	record := diagRecord(t, diagnostics)
	if record["logshipper"] != true {
		t.Errorf("Self_Diagnostic = %v, want the logshipper marker attribute", record)
	}
	if got := record["level"]; got != slog.LevelWarn.String() {
		t.Errorf("level = %v, want %s", got, slog.LevelWarn)
	}
	if got := record["msg"]; got != "diagnostic under test" {
		t.Errorf("msg = %v, want the message passed to diagf", got)
	}
	if got := record["detail"]; got != "value" {
		t.Errorf("detail = %v, want the attribute passed to diagf", got)
	}

	// The diagnostic logger is not level-gated by the Shipping_Threshold: an INFO
	// summary must appear even though the threshold resolved to ERROR (Req 11.9).
	if s.cfg.MinLevel != slog.LevelError {
		t.Fatalf("MinLevel = %s, want the ERROR default so the gate claim below is meaningful", s.cfg.MinLevel)
	}
	diagnostics.Reset()
	s.diagf(slog.LevelInfo, "below the shipping threshold")
	if got := diagRecord(t, diagnostics)["level"]; got != slog.LevelInfo.String() {
		t.Errorf("level = %v, want %s: the diagnostic logger has no level gate of its own", got, slog.LevelInfo)
	}
}

func TestDiagnosticsProduceZeroLogEvents(t *testing.T) {
	// Req 8.2 and 11.8, asserted with the feedback loop fully wired: the Shipper
	// is enabled, its Handler is installed as slog.Default() behind the fan-out,
	// and the diagnostics below are written at ERROR — at and above the
	// Shipping_Threshold, which is exactly the case Req 8.2 calls out. Zero
	// admissions and zero queued events is the whole claim.
	s, diagnostics, _ := newDiagShipper(t, 8)

	console := new(bytes.Buffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler())))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// One of every diagnostic this file's code can write, including the startup
	// summary and a drop WARN.
	s.diagf(slog.LevelError, "diagnostic at the shipping threshold")
	s.diagPanic("panic diagnostic", "boom")
	s.EmitStartupDiagnostics([]Diagnostic{
		{Level: slog.LevelError, Message: "deferred error diagnostic"},
	})
	s.maybeDiagDrop()

	if got := len(diagRecords(t, diagnostics)); got < 4 {
		t.Fatalf("wrote %d Self_Diagnostics, want at least the 4 emitted (%q)", got, diagnostics.String())
	}

	snap := s.Snapshot()
	if snap != (Snapshot{}) {
		t.Errorf("Snapshot() = %+v after writing diagnostics, want the zero Snapshot: a diagnostic must not be admitted (Req 8.2)", snap)
	}
	if got := queuedEvents(s); len(got) != 0 {
		t.Errorf("Event_Queue holds %d events after writing diagnostics, want 0 (Req 8.2)", len(got))
	}
	if console.Len() != 0 {
		t.Errorf("console handler wrote %q, want nothing: diagnostics go to the injected writer, not through slog.Default()", console)
	}
	// And no diagnostic named the token, on any of those paths (Req 7.13, 10.4).
	assertNoSecrets(t, diagnostics.String())
}

// assertNoSecrets fails when diagnostic output names the Source_Token or the
// endpoint query string (Req 7.13, 10.4, 10.5).
func assertNoSecrets(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, testDiagToken) {
		t.Errorf("diagnostic output contains the Source_Token: %q", output)
	}
	if strings.Contains(output, testDiagQuery) || strings.Contains(output, "QUERYSECRET") {
		t.Errorf("diagnostic output contains the endpoint query string: %q", output)
	}
	if strings.Contains(output, "user:pw") {
		t.Errorf("diagnostic output contains the endpoint userinfo: %q", output)
	}
}

func TestEmitStartupDiagnosticsWritesTheEnabledSummary(t *testing.T) {
	// Req 11.9: exactly one INFO diagnostic carrying the Shipping_Threshold name,
	// the maximum Delivery_Batch size, the flush interval in seconds, the
	// Event_Queue capacity, and the ingest host — and excluding the Source_Token
	// and the endpoint query string.
	cfg, _ := ResolveConfig(RawConfig{
		SourceToken:  testDiagToken,
		IngestURL:    testDiagURL,
		MinLevel:     "warn",
		BatchSize:    "37",
		FlushSeconds: "11",
		QueueSize:    "123",
	})
	diagnostics := new(bytes.Buffer)
	cfg.DiagWriter = diagnostics
	cfg.Clock = newFakeClock()
	s := NewShipper(cfg)

	s.EmitStartupDiagnostics(nil)

	record := diagRecord(t, diagnostics)
	if got := record["level"]; got != slog.LevelInfo.String() {
		t.Errorf("level = %v, want %s", got, slog.LevelInfo)
	}
	if record["logshipper"] != true {
		t.Errorf("startup summary = %v, want the logshipper marker", record)
	}
	if got := record["min_level"]; got != slog.LevelWarn.String() {
		t.Errorf("min_level = %v, want %s", got, slog.LevelWarn)
	}
	if got := diagNumber(t, record, "batch_size"); got != 37 {
		t.Errorf("batch_size = %v, want 37", got)
	}
	if got := diagNumber(t, record, "flush_seconds"); got != 11 {
		t.Errorf("flush_seconds = %v, want 11", got)
	}
	if got := diagNumber(t, record, "queue_capacity"); got != 123 {
		t.Errorf("queue_capacity = %v, want 123", got)
	}
	if got := record["ingest_host"]; got != testDiagHost {
		t.Errorf("ingest_host = %v, want %q: the host only, with no scheme, path, userinfo, or query", got, testDiagHost)
	}
	// The host only: a summary that leaked the path would carry the endpoint's
	// route, which can itself identify a source.
	if got, _ := record["ingest_host"].(string); strings.Contains(got, "/") {
		t.Errorf("ingest_host = %q, want no path component", got)
	}
	assertNoSecrets(t, diagnostics.String())
}

func TestEmitStartupDiagnosticsWritesEachDeferredDiagnosticAtItsLevel(t *testing.T) {
	// Req 1.4, 1.5, 1.6, 1.12: the deferred Diagnostics ResolveConfig returned
	// are written here, at the levels it recorded, and a disabled Shipper gets no
	// summary because it will transmit nothing (Req 11.9 applies to the enabled
	// state only).
	cfg, diags := ResolveConfig(RawConfig{
		IngestURL:   "http://plaintext.example.test/v1/logs", // Req 1.6: not https
		Environment: "Production",                            // Req 1.12, case-insensitive
		BatchSize:   "0",                                     // Req 5.4: out of range
	})
	if cfg.Enabled {
		t.Fatal("ResolveConfig enabled a Config with no token and a non-https URL")
	}
	if len(diags) == 0 {
		t.Fatal("ResolveConfig returned no diagnostics for a config with four problems")
	}

	diagnostics := new(bytes.Buffer)
	cfg.DiagWriter = diagnostics
	cfg.Clock = newFakeClock()
	s := NewShipper(cfg)

	s.EmitStartupDiagnostics(diags)

	records := diagRecords(t, diagnostics)
	if len(records) != len(diags) {
		t.Fatalf("wrote %d diagnostics for %d deferred ones, want one each and no summary: %q",
			len(records), len(diags), diagnostics.String())
	}
	for i, want := range diags {
		if got := records[i]["level"]; got != want.Level.String() {
			t.Errorf("diagnostic %d level = %v, want %s (%q)", i, got, want.Level, want.Message)
		}
		if got := records[i]["msg"]; got != want.Message {
			t.Errorf("diagnostic %d msg = %v, want %q", i, got, want.Message)
		}
		if records[i]["logshipper"] != true {
			t.Errorf("diagnostic %d = %v, want the logshipper marker", i, records[i])
		}
	}

	// The levels the decision table fixes, spot-checked by content rather than by
	// position, so a reordering inside ResolveConfig does not silently pass.
	byMessage := map[string]string{}
	for _, record := range records {
		msg, _ := record["msg"].(string)
		level, _ := record["level"].(string)
		byMessage[msg] = level
	}
	for _, tc := range []struct {
		contains string
		level    slog.Level
	}{
		{EnvSourceToken + " is not set", slog.LevelWarn}, // Req 1.4
		{EnvIngestURL + " is invalid", slog.LevelError},  // Req 1.6
		{"inactive in production", slog.LevelError},      // Req 1.12
		{"invalid " + EnvBatchSize, slog.LevelWarn},      // Req 5.4
	} {
		found := false
		for msg, level := range byMessage {
			if !strings.Contains(msg, tc.contains) {
				continue
			}
			found = true
			if level != tc.level.String() {
				t.Errorf("diagnostic %q was written at %s, want %s", msg, level, tc.level)
			}
		}
		if !found {
			t.Errorf("no diagnostic mentioning %q was written: %v", tc.contains, byMessage)
		}
	}

	// Req 11.9: no INFO summary for a disabled Shipper.
	for _, record := range records {
		if record["level"] == slog.LevelInfo.String() {
			t.Errorf("wrote an INFO diagnostic for a disabled Shipper: %v", record)
		}
	}
}

func TestEmitStartupDiagnosticsWithNoDiagnosticsAndDisabledWritesNothing(t *testing.T) {
	// The quiet default deployment: forwarding deliberately off, nothing to
	// report beyond what ResolveConfig already said.
	cfg, _ := ResolveConfig(RawConfig{})
	diagnostics := new(bytes.Buffer)
	cfg.DiagWriter = diagnostics
	s := NewShipper(cfg)

	s.EmitStartupDiagnostics(nil)

	if diagnostics.Len() != 0 {
		t.Errorf("wrote %q, want nothing", diagnostics)
	}
}

func TestMaybeDiagDropWritesOnceThenSuppressesForTheWindow(t *testing.T) {
	// Req 6.4 and 6.8 on the injected clock: one WARN per 60 s, carrying the
	// cumulative dropped count and the configured capacity, with every suppressed
	// drop still included in the next report.
	const capacity = 2
	s, diagnostics, clock := newDiagShipper(t, capacity)

	console := new(bytes.Buffer)
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(console, nil), s.Handler()))

	// capacity + 3 records: the first two fill the queue, the next three drop.
	emitErrors(logger, capacity+3, "queue full record")

	if got := s.Snapshot().DroppedByReason[reasonQueueFull]; got != 3 {
		t.Fatalf("DroppedByReason[reasonQueueFull] = %d, want 3", got)
	}

	first := diagRecord(t, diagnostics)
	if got := first["level"]; got != slog.LevelWarn.String() {
		t.Errorf("level = %v, want %s", got, slog.LevelWarn)
	}
	// The count is cumulative *as of the moment the diagnostic was written*,
	// which is the first drop: enqueue increments the counter before calling
	// maybeDiagDrop, so the drop being reported is itself included, and the two
	// drops that followed inside the window are reported by the next one
	// (Req 6.4, 6.8).
	if got := diagNumber(t, first, "dropped_total"); got != 1 {
		t.Errorf("dropped_total = %v, want 1: the cumulative count at the first drop, including that drop", got)
	}
	if got := diagNumber(t, first, "queue_capacity"); got != capacity {
		t.Errorf("queue_capacity = %v, want %d", got, capacity)
	}

	// One second short of the window: every further drop is silent, and each one
	// still increments the counter (Req 6.8).
	diagnostics.Reset()
	clock.Advance(dropDiagWindow - time.Second)
	emitErrors(logger, 4, "suppressed drop")
	if diagnostics.Len() != 0 {
		t.Errorf("wrote %q inside the %s window, want nothing (Req 6.8)", diagnostics, dropDiagWindow)
	}

	// Crossing the window: exactly one more WARN, reporting the drops suppressed
	// in between as part of the cumulative total.
	clock.Advance(time.Second)
	emitErrors(logger, 1, "after the window")

	second := diagRecord(t, diagnostics)
	if got := diagNumber(t, second, "dropped_total"); got != 8 {
		t.Errorf("dropped_total = %v, want the cumulative 8 (3 + 4 suppressed + 1)", got)
	}
	if got := diagNumber(t, second, "dropped_queue_full"); got != 8 {
		t.Errorf("dropped_queue_full = %v, want 8", got)
	}
	if got := s.Snapshot().DroppedByReason[reasonQueueFull]; got != 8 {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want 8", got)
	}

	// Req 6.5: the console wrote every record, dropped or not.
	if got := consoleLines(console); got != capacity+3+4+1 {
		t.Errorf("console wrote %d records, want %d", got, capacity+3+4+1)
	}
	assertNoSecrets(t, diagnostics.String())
}

func TestDropDiagLimiterAllowsOncePerWindowFromItsZeroValue(t *testing.T) {
	// The gate itself, without a Shipper: usable from its zero value, one claim
	// per window, and a claim exactly at the boundary.
	var limiter dropDiagLimiter
	base := fakeClockEpoch

	if !limiter.allow(base, dropDiagWindow) {
		t.Fatal("allow = false on the first call, want true: nothing has been written yet (Req 6.4)")
	}
	if limiter.allow(base, dropDiagWindow) {
		t.Error("allow = true for a second call at the same instant, want false")
	}
	if limiter.allow(base.Add(dropDiagWindow-time.Nanosecond), dropDiagWindow) {
		t.Error("allow = true one nanosecond before the window elapsed, want false")
	}
	if !limiter.allow(base.Add(dropDiagWindow), dropDiagWindow) {
		t.Error("allow = false exactly at the window boundary, want true")
	}
	// A clock that moved backwards suppresses rather than writing a burst.
	if limiter.allow(base, dropDiagWindow) {
		t.Error("allow = true for a timestamp before the last claim, want false")
	}
}

func TestMaybeDiagDropIsSafeUnderConcurrentDropsAndWritesOnce(t *testing.T) {
	// Req 6.3, 6.4, and 11.12 together: many producers can be in the drop path at
	// once, the rate-limit claim is atomic rather than locked, and the window is
	// still honoured — exactly one diagnostic, not one per goroutine. Run under
	// -race this also covers the diagnostic writer being shared.
	const (
		capacity  = 1
		producers = 8
		perGo     = 200
	)
	s, diagnostics, _ := newDiagShipper(t, capacity)
	ev := testEvent(t, "concurrent drop")

	done := make(chan struct{})
	for p := 0; p < producers; p++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perGo; i++ {
				s.enqueue(ev)
			}
		}()
	}
	for p := 0; p < producers; p++ {
		<-done
	}

	records := diagRecords(t, diagnostics)
	if len(records) != 1 {
		t.Fatalf("wrote %d Self_Diagnostics for a concurrent burst, want exactly 1 per window: %q",
			len(records), diagnostics.String())
	}
	if got, want := s.Snapshot().DroppedByReason[reasonQueueFull], uint64(producers*perGo-capacity); got != want {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want %d", got, want)
	}
}

func TestDiagWriterDefaultsToStandardError(t *testing.T) {
	// Req 12.8: standard error when no writer was supplied. os.Stderr is
	// redirected through a pipe so the fallback is observed rather than assumed.
	// Nothing in this package's tests runs in parallel, so the swap is contained.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() = %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = original })

	cfg, _ := ResolveConfig(RawConfig{SourceToken: testDiagToken, IngestURL: testDiagURL})
	if cfg.DiagWriter != writer {
		t.Fatalf("applyDefaults left DiagWriter = %#v, want os.Stderr (Req 12.8)", cfg.DiagWriter)
	}
	s := NewShipper(cfg)
	s.diagf(slog.LevelWarn, "written to the stderr default")

	// The nil-writer fallback inside newDiagLogger, for a Shipper composed
	// literally rather than through NewShipper.
	newDiagLogger(Config{}).Warn("written by the nil fallback")

	if err := writer.Close(); err != nil {
		t.Fatalf("closing the pipe writer = %v", err)
	}
	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the redirected stderr = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(captured)), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr holds %d lines, want 2: %q", len(lines), captured)
	}
	for i, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("stderr line %d is not JSON: %v (%q)", i, err, line)
		}
		if fields["logshipper"] != true {
			t.Errorf("stderr line %d = %v, want the logshipper marker", i, fields)
		}
	}
	if !strings.Contains(string(captured), "written to the stderr default") {
		t.Errorf("stderr = %q, want the diagnostic written through the default writer", captured)
	}
	if !strings.Contains(string(captured), "written by the nil fallback") {
		t.Errorf("stderr = %q, want the diagnostic written through newDiagLogger's nil fallback", captured)
	}
}

func TestIngestHostAndDiagEndpointStripSecrets(t *testing.T) {
	// Req 10.5: an endpoint in a diagnostic carries scheme, host, and path only.
	// Req 11.9: the startup summary carries the host alone.
	if got, want := ingestHost(testDiagURL), testDiagHost; got != want {
		t.Errorf("ingestHost(%q) = %q, want %q", testDiagURL, got, want)
	}
	if got := ingestHost("in.example.test/v1/logs"); got != unknownIngestHost {
		t.Errorf("ingestHost of a schemeless value = %q, want %q: a value that does not parse as a URL must not be echoed", got, unknownIngestHost)
	}
	if got, want := ingestHost("https://in.example.test:8443/v1"), "in.example.test:8443"; got != want {
		t.Errorf("ingestHost with a port = %q, want %q", got, want)
	}

	s, _, _ := newDiagShipper(t, 2)
	rendered := s.diagEndpoint()
	if got, want := rendered, "https://"+testDiagHost+"/v1/logs"; got != want {
		t.Errorf("diagEndpoint() = %q, want %q", got, want)
	}
	assertNoSecrets(t, rendered)
}
