package logging

import (
	crand "crypto/rand"
	"encoding/binary"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Environment variable names. They are only ever read by main.go through the
// configs package; the logging package itself reads no environment variable
// (Req 1.14, 12.1). The names live here so diagnostics can name the variable
// that was rejected.
const (
	EnvSourceToken  = "BETTERSTACK_SOURCE_TOKEN"
	EnvIngestURL    = "BETTERSTACK_INGEST_URL"
	EnvServiceName  = "BETTERSTACK_SERVICE_NAME"
	EnvMinLevel     = "BETTERSTACK_MIN_LEVEL"
	EnvBatchSize    = "BETTERSTACK_BATCH_SIZE"
	EnvFlushSeconds = "BETTERSTACK_FLUSH_SECONDS"
	EnvQueueSize    = "BETTERSTACK_QUEUE_SIZE"
)

// Resolved defaults and accepted ranges, straight from the design's
// disabled-state and validation decision table.
const (
	defaultServiceName    = "fundis-backend"
	maxServiceNameChars   = 64
	defaultBatchSize      = 20
	minBatchSize          = 1
	maxBatchSize          = 1000
	defaultFlushSeconds   = 5
	minFlushSeconds       = 1
	maxFlushSeconds       = 300
	defaultQueueSize      = 1000
	minQueueSize          = 1
	maxQueueSize          = 100000
	maxDiagValueChars     = 64
	productionEnvironment = "production"
)

// RawConfig holds unvalidated strings exactly as they were read from the
// process environment. main.go fills it from the configs accessors so that
// parsing, defaulting, and validation all happen in one pure place.
type RawConfig struct {
	SourceToken  string
	IngestURL    string
	ServiceName  string
	Environment  string
	MinLevel     string
	BatchSize    string
	FlushSeconds string
	QueueSize    string
}

// Config is the validated configuration a Shipper is constructed from. Enabled
// carries the resolved enabled/disabled state: a disabled Shipper starts no
// goroutine, opens no connection, and stays disabled for the lifetime of the
// process (Req 1.8, 1.11).
type Config struct {
	Enabled     bool
	IngestURL   string
	SourceToken string
	ServiceName string
	Environment string
	MinLevel    slog.Level
	BatchSize   int
	FlushEvery  time.Duration
	QueueSize   int

	DiagWriter io.Writer // nil => os.Stderr (Req 12.8)
	Clock      Clock     // nil => SystemClock()
	Jitter     Jitter    // nil => RandJitter(seed)
}

// Diagnostic is a deferred Self_Diagnostic. ResolveConfig returns these rather
// than writing them, so configuration validation is testable without a writer
// and without a Shipper. Args are slog-style alternating key/value pairs.
type Diagnostic struct {
	Level   slog.Level
	Message string
	Args    []any
}

// ResolveConfig turns raw environment strings into a validated Config plus the
// Self_Diagnostics that describe every rejected or defaulted value.
//
// It is pure: it reads no environment variable, performs no I/O, writes no
// diagnostic, and reads no clock. Every value is trimmed of leading and
// trailing whitespace before evaluation (Req 1.1, 1.2, 12.1).
//
// A configuration problem never fails: the returned Config is always usable,
// with Enabled reporting whether forwarding is active (Req 1.9).
func ResolveConfig(raw RawConfig) (Config, []Diagnostic) {
	var diags []Diagnostic

	token := strings.TrimSpace(raw.SourceToken)
	ingestURL := strings.TrimSpace(raw.IngestURL)
	environment := strings.TrimSpace(raw.Environment)

	cfg := Config{
		SourceToken: token,
		IngestURL:   ingestURL,
		Environment: environment,
	}

	// Source token: empty disables forwarding entirely (Req 1.4).
	tokenOK := token != ""
	if !tokenOK {
		diags = append(diags, Diagnostic{
			Level:   slog.LevelWarn,
			Message: "better stack forwarding disabled: " + EnvSourceToken + " is not set",
			Args:    []any{"variable", EnvSourceToken},
		})
	}

	// Ingest URL: empty disables forwarding (Req 1.5); a present but unusable
	// value is an ERROR because it signals a misconfiguration rather than a
	// deliberate opt-out (Req 1.6). The token never appears in either message
	// (Req 10.4).
	urlOK := false
	switch ingestURL {
	case "":
		diags = append(diags, Diagnostic{
			Level:   slog.LevelWarn,
			Message: "better stack forwarding disabled: " + EnvIngestURL + " is not set",
			Args:    []any{"variable", EnvIngestURL},
		})
	default:
		if reason, ok := validateIngestURL(ingestURL); ok {
			urlOK = true
		} else {
			diags = append(diags, Diagnostic{
				Level:   slog.LevelError,
				Message: "better stack forwarding disabled: " + EnvIngestURL + " is invalid",
				Args:    []any{"variable", EnvIngestURL, "reason", reason},
			})
		}
	}

	// Enabled only when both credentials pass validation (Req 1.7).
	cfg.Enabled = tokenOK && urlOK

	// Service name: default when unset, empty, or longer than 64 characters
	// (Req 1.3).
	serviceName := strings.TrimSpace(raw.ServiceName)
	if serviceName == "" || utf8.RuneCountInString(serviceName) > maxServiceNameChars {
		serviceName = defaultServiceName
	}
	cfg.ServiceName = serviceName

	// Shipping threshold: warn/error, case-insensitive. Unset or empty resolves
	// to ERROR with no diagnostic (Req 2.2); any other non-empty value resolves
	// to ERROR with one WARN naming the rejected value (Req 2.1, 2.8).
	minLevel := strings.TrimSpace(raw.MinLevel)
	switch strings.ToLower(minLevel) {
	case "":
		cfg.MinLevel = slog.LevelError
	case "warn":
		cfg.MinLevel = slog.LevelWarn
	case "error":
		cfg.MinLevel = slog.LevelError
	default:
		cfg.MinLevel = slog.LevelError
		diags = append(diags, Diagnostic{
			Level:   slog.LevelWarn,
			Message: "invalid " + EnvMinLevel + ", using error",
			Args: []any{
				"variable", EnvMinLevel,
				"rejected_value", truncateDiagValue(minLevel),
				"applied_value", "error",
			},
		})
	}

	// Numeric settings. An unset or empty variable takes the default silently;
	// only a non-empty rejected value produces a WARN (Req 5.4, 5.6, 6.1, 6.7).
	batchSize, batchDiag := resolveInt(raw.BatchSize, EnvBatchSize, defaultBatchSize, minBatchSize, maxBatchSize)
	cfg.BatchSize = batchSize
	if batchDiag != nil {
		diags = append(diags, *batchDiag)
	}

	flushSeconds, flushDiag := resolveInt(raw.FlushSeconds, EnvFlushSeconds, defaultFlushSeconds, minFlushSeconds, maxFlushSeconds)
	cfg.FlushEvery = time.Duration(flushSeconds) * time.Second
	if flushDiag != nil {
		diags = append(diags, *flushDiag)
	}

	queueSize, queueDiag := resolveInt(raw.QueueSize, EnvQueueSize, defaultQueueSize, minQueueSize, maxQueueSize)
	cfg.QueueSize = queueSize
	if queueDiag != nil {
		diags = append(diags, *queueDiag)
	}

	// Being disabled in production is an operational problem worth an ERROR,
	// because centralized error logging is the thing nobody notices is missing
	// (Req 1.12).
	if !cfg.Enabled && strings.EqualFold(environment, productionEnvironment) {
		diags = append(diags, Diagnostic{
			Level:   slog.LevelError,
			Message: "centralized error logging is inactive in production",
			Args:    []any{"environment", environment},
		})
	}

	return applyDefaults(cfg), diags
}

// applyDefaults fills the injectable collaborators that were left nil. Callers
// that want their own writer, clock, or jitter source overwrite the fields
// after ResolveConfig returns, which is what main.go does for DiagWriter.
func applyDefaults(cfg Config) Config {
	if cfg.DiagWriter == nil {
		cfg.DiagWriter = os.Stderr
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	if cfg.Jitter == nil {
		// Seeded from crypto/rand rather than math/rand's global source (which
		// is not guaranteed auto-seeded on every Go version — an unseeded
		// source hands every process the same jitter sequence, defeating the
		// point of jitter: spreading retries across instances) or the wall
		// clock, so ResolveConfig stays free of clock reads while separate
		// processes still spread their retries.
		cfg.Jitter = RandJitter(randomSeed())
	}
	return cfg
}

// randomSeed returns a seed drawn from crypto/rand, which stays off the
// math/rand global source (not guaranteed auto-seeded on every Go version)
// and off time.Now (reserved for the injected Clock per Req 12.2). A read
// failure is not expected in practice; b is left zeroed in that case, which
// still yields a valid, merely predictable, seed rather than a panic.
func randomSeed() int64 {
	var b [8]byte
	_, _ = crand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]))
}

// validateIngestURL reports whether an ingest endpoint is usable, and why not
// when it is not. A usable endpoint parses, uses the https scheme, and carries
// a host (Req 1.6). The returned reason never contains the endpoint itself, so
// no query string or userinfo component can leak into a diagnostic (Req 10.5).
func validateIngestURL(raw string) (reason string, ok bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "value is not a valid URL", false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "scheme must be https", false
	}
	if parsed.Host == "" {
		return "URL has no host", false
	}
	return "", true
}

// resolveInt parses a base-10 integer that must fall inside [min, max],
// returning the default and one WARN Diagnostic when a non-empty value is
// unparseable or out of range, and the default with no Diagnostic when the
// value is unset or empty.
func resolveInt(rawValue, variable string, defaultValue, minValue, maxValue int) (int, *Diagnostic) {
	value := strings.TrimSpace(rawValue)
	if value == "" {
		return defaultValue, nil
	}

	parsed, err := strconv.Atoi(value)
	if err == nil && parsed >= minValue && parsed <= maxValue {
		return parsed, nil
	}

	return defaultValue, &Diagnostic{
		Level:   slog.LevelWarn,
		Message: "invalid " + variable + ", using default",
		Args: []any{
			"variable", variable,
			"rejected_value", truncateDiagValue(value),
			"applied_value", defaultValue,
			"allowed_min", minValue,
			"allowed_max", maxValue,
		},
	}
}

// truncateDiagValue caps a rejected value at 64 characters so an oversized
// environment variable cannot bloat a diagnostic (Req 2.8).
func truncateDiagValue(s string) string {
	if utf8.RuneCountInString(s) <= maxDiagValueChars {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxDiagValueChars])
}
