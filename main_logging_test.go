package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"backend/logging"

	"github.com/labstack/echo/v4"
)

type mainRecoveryCapturedRecord struct {
	level         slog.Level
	message       string
	attrs         map[string]any
	requestID     string
	requestMethod string
	requestPath   string
}

type mainRecoveryCaptureState struct {
	mu      sync.Mutex
	records []mainRecoveryCapturedRecord
}

type mainRecoveryCaptureHandler struct {
	state *mainRecoveryCaptureState
}

func (h *mainRecoveryCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *mainRecoveryCaptureHandler) Handle(ctx context.Context, record slog.Record) error {
	captured := mainRecoveryCapturedRecord{
		level:   record.Level,
		message: record.Message,
		attrs:   make(map[string]any, record.NumAttrs()),
	}
	record.Attrs(func(attr slog.Attr) bool {
		captured.attrs[attr.Key] = attr.Value.Any()
		return true
	})
	captured.requestID, captured.requestMethod, captured.requestPath, _ = logging.RequestFromContext(ctx)

	h.state.mu.Lock()
	h.state.records = append(h.state.records, captured)
	h.state.mu.Unlock()
	return nil
}

func (h *mainRecoveryCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *mainRecoveryCaptureHandler) WithGroup(string) slog.Handler      { return h }

func (s *mainRecoveryCaptureState) snapshot() []mainRecoveryCapturedRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mainRecoveryCapturedRecord(nil), s.records...)
}

//go:noinline
func mainRecoveryPanicAtDepth(depth int, value string) {
	if depth == 0 {
		panic(value)
	}
	mainRecoveryPanicAtDepth(depth-1, value)
	if depth < 0 {
		panic("unreachable")
	}
}

// TestMainRecoveryEmitsOneBoundedCorrelatedError exercises the exact middleware
// setup installed by main, rather than duplicating its RecoverConfig in the test.
// It validates the single-record and response-header behavior required by 2.6
// and 4.2, plus the request correlation carried by that record.
func TestMainRecoveryEmitsOneBoundedCorrelatedError(t *testing.T) {
	capture := &mainRecoveryCaptureState{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(&mainRecoveryCaptureHandler{state: capture}))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	app := echo.New()
	app.Logger.SetOutput(io.Discard)
	configureApplicationLoggingMiddleware(app)

	panicText := strings.Repeat("panic-value-", 100)
	app.GET("/panic", func(echo.Context) error {
		mainRecoveryPanicAtDepth(512, panicText)
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Errorf("response status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	responseRequestID := response.Header().Get(echo.HeaderXRequestID)
	if responseRequestID == "" {
		t.Fatal("recovered panic response has no X-Request-Id header")
	}

	records := capture.snapshot()
	if len(records) != 1 {
		t.Fatalf("captured %d slog records, want exactly one ERROR record and no duplicate Echo recovery record: %+v", len(records), records)
	}
	record := records[0]
	if record.level != slog.LevelError {
		t.Errorf("record level = %s, want ERROR", record.level)
	}
	if record.message != "panic recovered" {
		t.Errorf("record message = %q, want %q", record.message, "panic recovered")
	}

	panicValue, ok := record.attrs["panic"].(string)
	if !ok {
		t.Fatalf("panic attribute = %#v, want a string", record.attrs["panic"])
	}
	wantPanic := string([]rune(panicText)[:1024])
	if panicValue != wantPanic {
		t.Errorf("panic attribute has %d characters and does not equal the first 1024 panic characters", len([]rune(panicValue)))
	}

	stack, ok := record.attrs["stack"].(string)
	if !ok {
		t.Fatalf("stack attribute = %#v, want a string", record.attrs["stack"])
	}
	if len(stack) != 8192 {
		t.Errorf("stack attribute = %d bytes, want the first 8192 bytes of the captured stack", len(stack))
	}
	if !strings.Contains(stack, "mainRecoveryPanicAtDepth") {
		t.Errorf("stack does not identify the panicking handler: %q", stack)
	}

	if record.requestID != responseRequestID {
		t.Errorf("record request id = %q, response X-Request-Id = %q", record.requestID, responseRequestID)
	}
	if record.requestMethod != http.MethodGet {
		t.Errorf("record request method = %q, want GET", record.requestMethod)
	}
	if record.requestPath != "/panic" {
		t.Errorf("record request path = %q, want registered route template /panic", record.requestPath)
	}
}
