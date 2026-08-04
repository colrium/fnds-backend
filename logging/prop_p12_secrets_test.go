package logging

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type p12Input struct {
	SourceToken     string
	EndpointSecret  string
	SensitiveKeyA   string
	SensitiveKeyB   string
	SensitiveValueA string
	SensitiveValueB string
	SensitiveValueC string
}

func p12LatestBody(t *testing.T, server *deliveryServer, previous int) []byte {
	t.Helper()
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.bodies) != previous+1 {
		t.Fatalf("server recorded %d bodies, want %d", len(server.bodies), previous+1)
	}
	return append([]byte(nil), server.bodies[previous]...)
}

func p12AssertAbsent(t *testing.T, seed int64, iteration int, input p12Input, where string, data []byte) {
	t.Helper()
	for _, secret := range []string{
		input.SourceToken,
		input.EndpointSecret,
		input.SensitiveValueA,
		input.SensitiveValueB,
		input.SensitiveValueC,
	} {
		if bytes.Contains(data, []byte(secret)) {
			pbtFailf(t, seed, iteration, input, "%s contains generated sensitive value %q: %q", where, secret, data)
		}
	}
}

// Feature: betterstack-error-logging, Property 12: Secrets never appear on the wire
// **Validates: Requirements 7.13, 10.1, 10.2, 10.3, 10.4, 10.11**
func TestProperty12_SecretsNeverAppearOnTheWireOrInDiagnostics(t *testing.T) {
	r, seed := pbtRand(t)
	server := newDeliveryServer(t, http.StatusRequestEntityTooLarge)

	for iteration := 0; iteration < pbtIterations; iteration++ {
		input := p12Input{
			SourceToken:     fmt.Sprintf("p12-token-%d-%016x", iteration, r.Uint64()),
			EndpointSecret:  fmt.Sprintf("p12-query-%d-%016x", iteration, r.Uint64()),
			SensitiveKeyA:   genSensitiveKey(r) + "_a",
			SensitiveKeyB:   genSensitiveKey(r) + "_b",
			SensitiveValueA: fmt.Sprintf("p12-sensitive-a-%d-%016x", iteration, r.Uint64()),
			SensitiveValueB: fmt.Sprintf("p12-sensitive-b-%d-%016x", iteration, r.Uint64()),
			SensitiveValueC: fmt.Sprintf("p12-sensitive-c-%d-%016x", iteration, r.Uint64()),
		}
		diagnostics := new(bytes.Buffer)
		s := NewShipper(Config{
			Enabled:     true,
			IngestURL:   server.URL + "/v1/logs?source_token=" + input.EndpointSecret,
			SourceToken: input.SourceToken,
			ServiceName: defaultServiceName,
			Environment: productionEnvironment,
			MinLevel:    slog.LevelError,
			BatchSize:   1,
			FlushEvery:  time.Minute,
			QueueSize:   2,
			DiagWriter:  diagnostics,
			Clock:       newFakeClock(),
			Jitter:      FixedJitter(0),
		})
		s.EmitStartupDiagnostics(nil)

		beforeRequests := server.requests()
		logger := deliveryLogger(s)
		logger.Error("p12 secret containment",
			slog.String(input.SensitiveKeyA, input.SensitiveValueA),
			slog.Group("outer",
				slog.String(strings.ToUpper(input.SensitiveKeyB), input.SensitiveValueB),
			),
			slog.String("detail", input.SourceToken),
			slog.Any(input.SensitiveKeyA+"_object", map[string]any{
				"nested": input.SensitiveValueC,
			}),
		)

		result := s.Close()
		if result.Discarded != 1 || result.Flushed != 0 || result.TimedOut {
			pbtFailf(t, seed, iteration, input, "Close result = %+v after HTTP 413, want one terminal discard", result)
		}
		body := p12LatestBody(t, server, beforeRequests)
		p12AssertAbsent(t, seed, iteration, input, "request body", body)
		p12AssertAbsent(t, seed, iteration, input, "Self_Diagnostics", diagnostics.Bytes())
		if !bytes.Contains(body, []byte(redactedPlaceholder)) {
			pbtFailf(t, seed, iteration, input, "request body contains no %s marker: %q", redactedPlaceholder, body)
		}
		if got := strings.Count(strings.TrimSpace(diagnostics.String()), "\n") + 1; got != 2 {
			pbtFailf(t, seed, iteration, input, "wrote %d diagnostics, want startup INFO plus one HTTP 413 ERROR: %q", got, diagnostics.String())
		}
	}
}
