package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type p1FilterInput struct {
	Threshold slog.Level
	Level     slog.Level
	Message   string
}

type p1RecordingEndpoint struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func p1NewRecordingEndpoint(t *testing.T) *p1RecordingEndpoint {
	t.Helper()
	endpoint := &p1RecordingEndpoint{}
	endpoint.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer func() { _ = request.Body.Close() }()
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		endpoint.mu.Lock()
		endpoint.bodies = append(endpoint.bodies, body)
		endpoint.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(endpoint.Close)
	return endpoint
}

func (e *p1RecordingEndpoint) deliveredMessages(t *testing.T) map[string]int {
	t.Helper()
	e.mu.Lock()
	bodies := append([][]byte(nil), e.bodies...)
	e.mu.Unlock()

	delivered := make(map[string]int)
	for requestIndex, body := range bodies {
		var events []struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &events); err != nil {
			t.Fatalf("P1 request %d is not a JSON array of events: %v", requestIndex, err)
		}
		for _, event := range events {
			delivered[event.Message]++
		}
	}
	return delivered
}

func p1NewWiredShipper(t *testing.T, endpoint *p1RecordingEndpoint, threshold slog.Level, capacity int) *Shipper {
	t.Helper()
	minLevel := "error"
	if threshold == slog.LevelWarn {
		minLevel = "warn"
	}
	cfg, diagnostics := ResolveConfig(RawConfig{
		SourceToken:  "p1-source-token",
		IngestURL:    endpoint.URL,
		ServiceName:  "p1-filter",
		Environment:  "test",
		MinLevel:     minLevel,
		BatchSize:    fmt.Sprintf("%d", capacity),
		FlushSeconds: "300",
		QueueSize:    fmt.Sprintf("%d", capacity),
	})
	if !cfg.Enabled || len(diagnostics) != 0 {
		t.Fatalf("P1 config for %s resolved to enabled=%t with diagnostics=%+v", threshold, cfg.Enabled, diagnostics)
	}
	cfg.DiagWriter = io.Discard
	cfg.Jitter = FixedJitter(0)

	shipper := NewShipper(cfg)
	// Keep the Shipper's one long-lived http.Client, replacing only its transport
	// with httptest's trusted TLS transport.
	shipper.client.http.Transport = endpoint.Client().Transport
	shipper.Start()
	return shipper
}

func p1ReferenceIncludes(level, threshold slog.Level) bool { return level >= threshold }

func p1MultisetsEqual(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, count := range left {
		if right[key] != count {
			return false
		}
	}
	return true
}

// Feature: betterstack-error-logging, Property 1: Severity filter is a faithful filter
// **Validates: Requirements 2.3, 2.4, 2.9, 2.10**
func TestProperty1_SeverityFilterIsAFaithfulFilter(t *testing.T) {
	random, seed := pbtRand(t)

	// Deterministic boundary cases run first and force each threshold to observe
	// a record immediately below, exactly at, and above its boundary.
	inputs := []p1FilterInput{
		{Threshold: slog.LevelWarn, Level: slog.LevelInfo, Message: "p1-boundary-warn-below"},
		{Threshold: slog.LevelWarn, Level: slog.LevelWarn, Message: "p1-boundary-warn-equal"},
		{Threshold: slog.LevelWarn, Level: slog.LevelError, Message: "p1-boundary-warn-above"},
		{Threshold: slog.LevelError, Level: slog.LevelWarn, Message: "p1-boundary-error-below"},
		{Threshold: slog.LevelError, Level: slog.LevelError, Message: "p1-boundary-error-equal"},
		{Threshold: slog.LevelError, Level: slog.LevelError + 2, Message: "p1-boundary-error-above"},
	}
	for iteration := 0; iteration < pbtIterations; iteration++ {
		threshold := slog.LevelError
		if random.Intn(2) == 0 {
			threshold = slog.LevelWarn
		}
		inputs = append(inputs, p1FilterInput{
			Threshold: threshold,
			Level:     genLevel(random),
			Message:   fmt.Sprintf("p1-random-%03d", iteration),
		})
	}

	endpoint := p1NewRecordingEndpoint(t)
	capacity := len(inputs) + 1
	shippers := map[slog.Level]*Shipper{
		slog.LevelWarn:  p1NewWiredShipper(t, endpoint, slog.LevelWarn, capacity),
		slog.LevelError: p1NewWiredShipper(t, endpoint, slog.LevelError, capacity),
	}
	loggers := map[slog.Level]*slog.Logger{}
	for threshold, shipper := range shippers {
		loggers[threshold] = slog.New(NewFanoutHandler(
			slog.NewJSONHandler(io.Discard, nil),
			shipper.Handler(),
		))
	}

	expected := make(map[string]int)
	expectedByThreshold := map[slog.Level]uint64{slog.LevelWarn: 0, slog.LevelError: 0}
	coverage := map[slog.Level]map[string]int{
		slog.LevelWarn:  {"below": 0, "equal": 0, "above": 0},
		slog.LevelError: {"below": 0, "equal": 0, "above": 0},
	}
	for _, input := range inputs {
		relation := "equal"
		if input.Level < input.Threshold {
			relation = "below"
		} else if input.Level > input.Threshold {
			relation = "above"
		}
		coverage[input.Threshold][relation]++

		loggers[input.Threshold].Log(context.Background(), input.Level, input.Message)
		if p1ReferenceIncludes(input.Level, input.Threshold) {
			expected[input.Message]++
			expectedByThreshold[input.Threshold]++
		}
	}

	for threshold, shipper := range shippers {
		result := shipper.Flush(3 * time.Second)
		if !result.Drained {
			pbtFatalf(t, seed, -1, inputs, "P1 %s Shipper did not drain: result=%+v snapshot=%+v", threshold, result, shipper.Snapshot())
		}
		if result.Dropped != 0 {
			pbtFailf(t, seed, -1, inputs, "P1 %s Shipper dropped %d events on a 202 endpoint", threshold, result.Dropped)
		}
		if got := shipper.Snapshot().Delivered; got != expectedByThreshold[threshold] {
			pbtFailf(t, seed, -1, inputs, "P1 %s delivered counter = %d, reference filter expects %d", threshold, got, expectedByThreshold[threshold])
		}
		shipper.Close()
	}

	for threshold, relations := range coverage {
		for relation, count := range relations {
			if count == 0 {
				pbtFatalf(t, seed, -1, inputs, "P1 coverage is vacuous for threshold %s relation %s", threshold, relation)
			}
		}
	}

	delivered := endpoint.deliveredMessages(t)
	if !p1MultisetsEqual(delivered, expected) {
		pbtFailf(t, seed, -1, inputs, "delivered multiset = %v, one-line reference filter expects %v", delivered, expected)
	}
}
