package logging

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func closeConfig(url string, diagnostics io.Writer, clock Clock) Config {
	return Config{
		Enabled:     true,
		IngestURL:   url,
		SourceToken: "close-test-source-token",
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   2,
		FlushEvery:  time.Minute,
		QueueSize:   8,
		DiagWriter:  diagnostics,
		Clock:       clock,
		Jitter:      FixedJitter(0),
	}
}

func TestCloseDisabledReturnsUnderTenMillisecondsWithoutRequestsOrDiagnostics(t *testing.T) {
	server := p10NewServer(t)
	diagnostics := new(sdDiag)
	cfg := closeConfig(server.URL, diagnostics, newFakeClock())
	cfg.Enabled = false
	s := NewShipper(cfg)

	started := time.Now()
	result := s.Close()
	elapsed := time.Since(started)

	if elapsed >= 10*time.Millisecond {
		t.Errorf("disabled Close took %s, want under 10ms (Req 9.10)", elapsed)
	}
	if result != (CloseResult{}) {
		t.Errorf("disabled Close result = %+v, want zero", result)
	}
	if got := server.count(); got != 0 {
		t.Errorf("disabled Close made %d requests, want 0", got)
	}
	if got := diagnostics.text(); got != "" {
		t.Errorf("disabled Close wrote diagnostics %q, want none", got)
	}
}

func TestCloseBudgetExpiryDiscardsRemainderAndWritesExactlyOneError(t *testing.T) {
	server := p10NewServer(t)
	server.block()
	diagnostics := new(sdDiag)
	clock := newFakeClock()
	s := NewShipper(closeConfig(server.URL, diagnostics, clock))
	p10Queue(s, 3)

	resultCh := make(chan CloseResult, 1)
	go func() { resultCh <- s.Close() }()

	if !waitFor(func() bool { return server.count() == 1 }, deliveryWaitTimeout) {
		server.release()
		t.Fatalf("close drain did not start its first request within %s", deliveryWaitTimeout)
	}
	if !clock.WaitAfters(1, deliveryWaitTimeout) {
		server.release()
		t.Fatalf("Close did not register its fixed budget on the fake clock")
	}
	clock.Advance(closeDrainBudget)

	var result CloseResult
	select {
	case result = <-resultCh:
	case <-time.After(deliveryWaitTimeout):
		server.release()
		t.Fatalf("Close did not return after the fake clock reached its budget")
	}
	server.release()

	if result != (CloseResult{Discarded: 3, TimedOut: true}) {
		t.Errorf("Close result = %+v, want Discarded=3 TimedOut=true", result)
	}
	if got := server.count(); got != 1 {
		t.Errorf("budget-expired Close made %d requests, want one attempt for the first batch", got)
	}
	snap := s.Snapshot()
	if snap.Dropped != 3 || snap.DroppedByReason[reasonAbandonedBatch] != 3 || snap.Delivered != 0 || snap.QueueDepth != 0 || snap.InFlight != 0 {
		t.Errorf("snapshot after close expiry = %+v, want all 3 events discarded as abandoned with no remainder", snap)
	}
	assertCounterIdentities(t, "after close budget expiry", snap)

	records := diagnostics.records(t)
	if len(records) != 1 {
		t.Fatalf("close budget expiry wrote %d diagnostics, want exactly 1: %q", len(records), diagnostics.text())
	}
	record := records[0]
	if record["level"] != slog.LevelError.String() {
		t.Errorf("close budget diagnostic level = %v, want ERROR", record["level"])
	}
	if record["msg"] != closeBudgetDiagMessage {
		t.Errorf("close budget diagnostic message = %v, want %q", record["msg"], closeBudgetDiagMessage)
	}
	if record["discarded_events"] != float64(3) {
		t.Errorf("close budget diagnostic discarded_events = %v, want 3", record["discarded_events"])
	}
	if strings.Contains(diagnostics.text(), s.cfg.SourceToken) {
		t.Errorf("close budget diagnostic leaked the Source_Token: %q", diagnostics.text())
	}
}

func TestEnqueueAfterCloseDropsWhileConsoleFanoutStillWrites(t *testing.T) {
	server := p10NewServer(t)
	s := NewShipper(closeConfig(server.URL, io.Discard, newFakeClock()))
	var console bytes.Buffer
	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(&console, nil), s.Handler()))

	if result := s.Close(); result != (CloseResult{}) {
		t.Fatalf("empty Close result = %+v, want zero", result)
	}
	beforeRequests := server.count()
	logger.Error("record after close", slog.String("detail", "still on console"))

	snap := s.Snapshot()
	if snap.Enqueued != 1 || snap.Dropped != 1 || snap.DroppedByReason[reasonAfterClose] != 1 {
		t.Errorf("snapshot after enqueue following Close = %+v, want one admission and one after-close drop", snap)
	}
	if snap.Delivered != 0 || snap.QueueDepth != 0 || snap.InFlight != 0 {
		t.Errorf("post-close event reached delivery state: %+v", snap)
	}
	assertCounterIdentities(t, "after enqueue following Close", snap)
	if got := server.count(); got != beforeRequests {
		t.Errorf("enqueue after Close added %d requests, want 0", got-beforeRequests)
	}
	if got := consoleLines(&console); got != 1 {
		t.Errorf("console wrote %d lines, want the post-close record", got)
	}
	if !strings.Contains(console.String(), "record after close") {
		t.Errorf("console output %q does not contain the post-close record", console.String())
	}
}
