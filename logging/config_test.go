package logging

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This file is the executable form of the design's disabled-state and
// validation decision table. ResolveConfig is pure, so every row is a plain
// value-in/value-out case: no environment variable is set, read, or restored
// anywhere in this file (Req 12.1). TestResolveConfigTestMutatesNoEnvironment
// enforces that mechanically.
//
// Diagnostic COUNT is asserted alongside level and content on every row. The
// rows for unset optional variables depend on it: the design deliberately emits
// no diagnostic when BETTERSTACK_MIN_LEVEL, _BATCH_SIZE, _FLUSH_SECONDS, or
// _QUEUE_SIZE is unset, so a spurious extra warning must fail (Req 2.2, 6.7).

const (
	// A distinctive token, so a leak into any diagnostic is unmistakable.
	testSourceToken = "s3cr3t-source-token"
	testIngestURL   = "https://in.logs.example.com/logs"
)

// validRaw is the all-valid baseline: forwarding enabled, every optional
// variable unset. A case overwrites only the field it exercises.
func validRaw() RawConfig {
	return RawConfig{SourceToken: testSourceToken, IngestURL: testIngestURL}
}

// wantDiag is an expected Self_Diagnostic: its level, substrings that must
// appear in its message or arguments, and substrings that must not.
type wantDiag struct {
	level    slog.Level
	contains []string
	absent   []string
}

func warnDiag(contains ...string) wantDiag {
	return wantDiag{level: slog.LevelWarn, contains: contains}
}

func errorDiag(contains ...string) wantDiag {
	return wantDiag{level: slog.LevelError, contains: contains}
}

// diagText renders a Diagnostic's message and every argument into one
// searchable string, so a content assertion covers the message and the args
// together.
func diagText(d Diagnostic) string {
	var b strings.Builder
	b.WriteString(d.Message)
	for _, arg := range d.Args {
		fmt.Fprintf(&b, " %v", arg)
	}
	return b.String()
}

func formatDiags(diags []Diagnostic) string {
	if len(diags) == 0 {
		return "  (none)"
	}
	lines := make([]string, 0, len(diags))
	for i, d := range diags {
		lines = append(lines, fmt.Sprintf("  [%d] %s %s", i, d.Level, diagText(d)))
	}
	return strings.Join(lines, "\n")
}

func assertDiags(t *testing.T, got []Diagnostic, want []wantDiag) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d diagnostics, want %d:\n%s", len(got), len(want), formatDiags(got))
	}
	for i, w := range want {
		text := diagText(got[i])
		if got[i].Level != w.level {
			t.Errorf("diagnostic %d level = %s, want %s (%q)", i, got[i].Level, w.level, text)
		}
		for _, sub := range w.contains {
			if !strings.Contains(text, sub) {
				t.Errorf("diagnostic %d = %q, want it to contain %q", i, text, sub)
			}
		}
		for _, sub := range w.absent {
			if strings.Contains(text, sub) {
				t.Errorf("diagnostic %d = %q, want it to exclude %q", i, text, sub)
			}
		}
	}
}

// TestResolveConfigSourceTokenAndIngestURL covers the first four rows of the
// decision table: the two disabling WARN rows, the invalid-URL ERROR row, and
// the enabled row (Req 1.4, 1.5, 1.6, 1.7). Trimming is asserted on every row
// (Req 1.1, 1.2).
func TestResolveConfigSourceTokenAndIngestURL(t *testing.T) {
	cases := []struct {
		name        string
		token       string
		ingestURL   string
		wantEnabled bool
		wantDiags   []wantDiag
	}{
		{
			name:        "both valid enables forwarding",
			token:       testSourceToken,
			ingestURL:   testIngestURL,
			wantEnabled: true,
		},
		{
			name:        "both valid after trimming enables forwarding",
			token:       "  " + testSourceToken + "\t",
			ingestURL:   " \n" + testIngestURL + "  ",
			wantEnabled: true,
		},
		{
			name:      "unset token disables with one warn naming the variable",
			token:     "",
			ingestURL: testIngestURL,
			wantDiags: []wantDiag{warnDiag(EnvSourceToken)},
		},
		{
			name:      "whitespace-only token counts as unset",
			token:     " \t\n ",
			ingestURL: testIngestURL,
			wantDiags: []wantDiag{warnDiag(EnvSourceToken)},
		},
		{
			name:      "unset ingest url disables with one warn naming the variable",
			token:     testSourceToken,
			ingestURL: "",
			wantDiags: []wantDiag{warnDiag(EnvIngestURL)},
		},
		{
			name:      "whitespace-only ingest url counts as unset",
			token:     testSourceToken,
			ingestURL: "   ",
			wantDiags: []wantDiag{warnDiag(EnvIngestURL)},
		},
		{
			name:      "both unset produces one warn each",
			token:     "",
			ingestURL: "",
			wantDiags: []wantDiag{warnDiag(EnvSourceToken), warnDiag(EnvIngestURL)},
		},
		{
			name:      "unparseable ingest url is an error",
			token:     testSourceToken,
			ingestURL: "https://%zz/logs",
			wantDiags: []wantDiag{errorDiag(EnvIngestURL)},
		},
		{
			name:      "ingest url with a space in the host is an error",
			token:     testSourceToken,
			ingestURL: "https://in logs.example.com/logs",
			wantDiags: []wantDiag{errorDiag(EnvIngestURL)},
		},
		{
			name:      "http scheme is an error",
			token:     testSourceToken,
			ingestURL: "http://in.logs.example.com/logs",
			wantDiags: []wantDiag{errorDiag(EnvIngestURL)},
		},
		{
			name:      "ingest url without a host is an error",
			token:     testSourceToken,
			ingestURL: "https:///logs",
			wantDiags: []wantDiag{errorDiag(EnvIngestURL)},
		},
		{
			name:      "scheme-only ingest url is an error",
			token:     testSourceToken,
			ingestURL: "https:",
			wantDiags: []wantDiag{errorDiag(EnvIngestURL)},
		},
		{
			name:      "unset token and invalid url produce a warn then an error",
			token:     "",
			ingestURL: "http://in.logs.example.com/logs",
			wantDiags: []wantDiag{warnDiag(EnvSourceToken), errorDiag(EnvIngestURL)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := RawConfig{SourceToken: tc.token, IngestURL: tc.ingestURL}

			cfg, diags := ResolveConfig(raw)

			if cfg.Enabled != tc.wantEnabled {
				t.Errorf("Enabled = %t, want %t (diagnostics:\n%s)", cfg.Enabled, tc.wantEnabled, formatDiags(diags))
			}
			if want := strings.TrimSpace(tc.token); cfg.SourceToken != want {
				t.Errorf("SourceToken = %q, want the trimmed value %q", cfg.SourceToken, want)
			}
			if want := strings.TrimSpace(tc.ingestURL); cfg.IngestURL != want {
				t.Errorf("IngestURL = %q, want the trimmed value %q", cfg.IngestURL, want)
			}
			assertDiags(t, diags, tc.wantDiags)
		})
	}
}

// TestResolveConfigInvalidURLDiagnosticsExcludeSecrets checks that the
// invalid-URL ERROR names the variable without leaking the Source_Token
// (Req 1.6, 10.4) or the endpoint's query string and userinfo (Req 10.5).
func TestResolveConfigInvalidURLDiagnosticsExcludeSecrets(t *testing.T) {
	const (
		userinfo    = "opsuser:opspassword"
		queryString = "source_token=" + testSourceToken + "&trace=abc123"
	)

	cases := []struct {
		name      string
		ingestURL string
	}{
		{name: "unparseable", ingestURL: "https://" + userinfo + "@%zz/logs?" + queryString},
		{name: "wrong scheme", ingestURL: "http://" + userinfo + "@in.logs.example.com/logs?" + queryString},
		{name: "no host", ingestURL: "https:///logs?" + queryString},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validRaw()
			raw.IngestURL = tc.ingestURL

			cfg, diags := ResolveConfig(raw)

			if cfg.Enabled {
				t.Fatalf("Enabled = true for ingest url %q, want false", tc.ingestURL)
			}
			assertDiags(t, diags, []wantDiag{{
				level:    slog.LevelError,
				contains: []string{EnvIngestURL},
				absent: []string{
					testSourceToken,
					queryString,
					"trace=abc123",
					"opsuser",
					"opspassword",
				},
			}})
		})
	}
}

// TestResolveConfigServiceName covers the service-name row: unset, empty, or
// longer than 64 characters falls back to fundis-backend (Req 1.3).
func TestResolveConfigServiceName(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "unset uses the default", value: "", want: defaultServiceName},
		{name: "whitespace-only uses the default", value: "  \t ", want: defaultServiceName},
		{name: "normal value passes through", value: "fundis-worker", want: "fundis-worker"},
		{name: "value is trimmed", value: "  fundis-worker  ", want: "fundis-worker"},
		{name: "exactly 64 characters passes through", value: strings.Repeat("s", 64), want: strings.Repeat("s", 64)},
		{name: "65 characters uses the default", value: strings.Repeat("s", 65), want: defaultServiceName},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validRaw()
			raw.ServiceName = tc.value

			cfg, diags := ResolveConfig(raw)

			if cfg.ServiceName != tc.want {
				t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, tc.want)
			}
			// The decision table lists no diagnostic for any service-name row.
			assertDiags(t, diags, nil)
		})
	}
}

// TestResolveConfigMinLevel covers the three Shipping_Threshold rows: unset
// resolves to ERROR silently (Req 2.2), warn/error map case-insensitively
// (Req 2.1), and anything else resolves to ERROR with one WARN carrying the
// rejected value truncated to 64 characters (Req 2.8).
func TestResolveConfigMinLevel(t *testing.T) {
	longValue := strings.Repeat("x", 100)

	cases := []struct {
		name      string
		value     string
		want      slog.Level
		wantDiags []wantDiag
	}{
		{name: "unset resolves to error with no diagnostic", value: "", want: slog.LevelError},
		{name: "whitespace-only resolves to error with no diagnostic", value: " \t ", want: slog.LevelError},
		{name: "warn", value: "warn", want: slog.LevelWarn},
		{name: "uppercase WARN", value: "WARN", want: slog.LevelWarn},
		{name: "padded mixed-case Warn", value: " Warn ", want: slog.LevelWarn},
		{name: "error", value: "error", want: slog.LevelError},
		{name: "uppercase ERROR", value: "ERROR", want: slog.LevelError},
		{
			name:      "unrecognised value warns and falls back to error",
			value:     "verbose",
			want:      slog.LevelError,
			wantDiags: []wantDiag{warnDiag(EnvMinLevel, "verbose")},
		},
		{
			name:  "info is not an accepted threshold",
			value: "info",
			want:  slog.LevelError,
			// slog knows INFO; the Shipping_Threshold does not accept it.
			wantDiags: []wantDiag{warnDiag(EnvMinLevel, "info")},
		},
		{
			name:  "oversized value is truncated to 64 characters",
			value: longValue,
			want:  slog.LevelError,
			wantDiags: []wantDiag{{
				level:    slog.LevelWarn,
				contains: []string{EnvMinLevel, strings.Repeat("x", 64)},
				absent:   []string{strings.Repeat("x", 65)},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validRaw()
			raw.MinLevel = tc.value

			cfg, diags := ResolveConfig(raw)

			if cfg.MinLevel != tc.want {
				t.Errorf("MinLevel = %s, want %s", cfg.MinLevel, tc.want)
			}
			if !cfg.Enabled {
				t.Errorf("Enabled = false, want true: a rejected threshold must not disable forwarding")
			}
			assertDiags(t, diags, tc.wantDiags)
		})
	}
}

// TestResolveConfigBatchSize covers the two BETTERSTACK_BATCH_SIZE rows,
// including both range boundaries. Unset takes the default with zero
// diagnostics, which is the design's flagged interpretation of Req 5.4.
func TestResolveConfigBatchSize(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		want      int
		wantDiags []wantDiag
	}{
		{name: "unset uses the default silently", value: "", want: defaultBatchSize},
		{name: "whitespace-only uses the default silently", value: "  ", want: defaultBatchSize},
		{name: "in-range value is accepted", value: "250", want: 250},
		{name: "padded value is accepted", value: " 250 ", want: 250},
		{name: "lower boundary is accepted", value: "1", want: 1},
		{name: "upper boundary is accepted", value: "1000", want: 1000},
		{
			name:      "one below the range is rejected",
			value:     "0",
			want:      defaultBatchSize,
			wantDiags: []wantDiag{warnDiag(EnvBatchSize, "0")},
		},
		{
			name:      "one above the range is rejected",
			value:     "1001",
			want:      defaultBatchSize,
			wantDiags: []wantDiag{warnDiag(EnvBatchSize, "1001")},
		},
		{
			name:      "negative value is rejected",
			value:     "-5",
			want:      defaultBatchSize,
			wantDiags: []wantDiag{warnDiag(EnvBatchSize, "-5")},
		},
		{
			name:      "unparseable value is rejected",
			value:     "twenty",
			want:      defaultBatchSize,
			wantDiags: []wantDiag{warnDiag(EnvBatchSize, "twenty")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validRaw()
			raw.BatchSize = tc.value

			cfg, diags := ResolveConfig(raw)

			if cfg.BatchSize != tc.want {
				t.Errorf("BatchSize = %d, want %d", cfg.BatchSize, tc.want)
			}
			assertDiags(t, diags, tc.wantDiags)
		})
	}
}

// TestResolveConfigFlushSeconds covers the two BETTERSTACK_FLUSH_SECONDS rows,
// including both range boundaries (Req 5.6).
func TestResolveConfigFlushSeconds(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		want      time.Duration
		wantDiags []wantDiag
	}{
		{name: "unset uses the default silently", value: "", want: defaultFlushSeconds * time.Second},
		{name: "whitespace-only uses the default silently", value: " \t", want: defaultFlushSeconds * time.Second},
		{name: "in-range value is accepted", value: "30", want: 30 * time.Second},
		{name: "padded value is accepted", value: " 30 ", want: 30 * time.Second},
		{name: "lower boundary is accepted", value: "1", want: time.Second},
		{name: "upper boundary is accepted", value: "300", want: 300 * time.Second},
		{
			name:      "one below the range is rejected",
			value:     "0",
			want:      defaultFlushSeconds * time.Second,
			wantDiags: []wantDiag{warnDiag(EnvFlushSeconds, "0")},
		},
		{
			name:      "one above the range is rejected",
			value:     "301",
			want:      defaultFlushSeconds * time.Second,
			wantDiags: []wantDiag{warnDiag(EnvFlushSeconds, "301")},
		},
		{
			name:      "unparseable value is rejected",
			value:     "5s",
			want:      defaultFlushSeconds * time.Second,
			wantDiags: []wantDiag{warnDiag(EnvFlushSeconds, "5s")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validRaw()
			raw.FlushSeconds = tc.value

			cfg, diags := ResolveConfig(raw)

			if cfg.FlushEvery != tc.want {
				t.Errorf("FlushEvery = %s, want %s", cfg.FlushEvery, tc.want)
			}
			assertDiags(t, diags, tc.wantDiags)
		})
	}
}

// TestResolveConfigQueueSize covers the two BETTERSTACK_QUEUE_SIZE rows. The
// rejection WARN must name the variable and the applied capacity of 1000
// (Req 6.1, 6.7).
func TestResolveConfigQueueSize(t *testing.T) {
	appliedCapacity := fmt.Sprint(defaultQueueSize)

	cases := []struct {
		name      string
		value     string
		want      int
		wantDiags []wantDiag
	}{
		{name: "unset uses the default silently", value: "", want: defaultQueueSize},
		{name: "whitespace-only uses the default silently", value: "   ", want: defaultQueueSize},
		{name: "in-range value is accepted", value: "5000", want: 5000},
		{name: "padded value is accepted", value: " 5000 ", want: 5000},
		{name: "lower boundary is accepted", value: "1", want: 1},
		{name: "upper boundary is accepted", value: "100000", want: 100000},
		{
			name:      "one below the range is rejected",
			value:     "0",
			want:      defaultQueueSize,
			wantDiags: []wantDiag{warnDiag(EnvQueueSize, "0", appliedCapacity)},
		},
		{
			name:      "one above the range is rejected",
			value:     "100001",
			want:      defaultQueueSize,
			wantDiags: []wantDiag{warnDiag(EnvQueueSize, "100001", appliedCapacity)},
		},
		{
			name:      "unparseable value is rejected",
			value:     "1_000",
			want:      defaultQueueSize,
			wantDiags: []wantDiag{warnDiag(EnvQueueSize, "1_000", appliedCapacity)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validRaw()
			raw.QueueSize = tc.value

			cfg, diags := ResolveConfig(raw)

			if cfg.QueueSize != tc.want {
				t.Errorf("QueueSize = %d, want %d", cfg.QueueSize, tc.want)
			}
			assertDiags(t, diags, tc.wantDiags)
		})
	}
}

// TestResolveConfigProductionWhileDisabled covers the APP_ENV row: a disabled
// Shipper in production earns an additional ERROR, and an enabled one earns
// none (Req 1.12).
func TestResolveConfigProductionWhileDisabled(t *testing.T) {
	const productionDiagText = "centralized error logging is inactive"

	cases := []struct {
		name        string
		token       string
		ingestURL   string
		environment string
		wantEnabled bool
		wantDiags   []wantDiag
	}{
		{
			name:        "disabled in production adds an error",
			ingestURL:   testIngestURL,
			environment: "production",
			wantDiags: []wantDiag{
				warnDiag(EnvSourceToken),
				errorDiag(productionDiagText),
			},
		},
		{
			name:        "case-insensitive PRODUCTION adds an error",
			ingestURL:   testIngestURL,
			environment: "PRODUCTION",
			wantDiags: []wantDiag{
				warnDiag(EnvSourceToken),
				errorDiag(productionDiagText),
			},
		},
		{
			name:        "padded mixed-case Production adds an error",
			ingestURL:   testIngestURL,
			environment: " Production ",
			wantDiags: []wantDiag{
				warnDiag(EnvSourceToken),
				errorDiag(productionDiagText),
			},
		},
		{
			name:        "invalid url in production yields both errors",
			token:       testSourceToken,
			ingestURL:   "http://in.logs.example.com/logs",
			environment: "production",
			wantDiags: []wantDiag{
				errorDiag(EnvIngestURL),
				errorDiag(productionDiagText),
			},
		},
		{
			name:        "enabled in production adds no diagnostic",
			token:       testSourceToken,
			ingestURL:   testIngestURL,
			environment: "production",
			wantEnabled: true,
		},
		{
			name:        "disabled outside production adds no extra diagnostic",
			ingestURL:   testIngestURL,
			environment: "staging",
			wantDiags:   []wantDiag{warnDiag(EnvSourceToken)},
		},
		{
			name:      "disabled with no environment adds no extra diagnostic",
			ingestURL: testIngestURL,
			wantDiags: []wantDiag{warnDiag(EnvSourceToken)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := RawConfig{SourceToken: tc.token, IngestURL: tc.ingestURL, Environment: tc.environment}

			cfg, diags := ResolveConfig(raw)

			if cfg.Enabled != tc.wantEnabled {
				t.Errorf("Enabled = %t, want %t", cfg.Enabled, tc.wantEnabled)
			}
			if want := strings.TrimSpace(tc.environment); cfg.Environment != want {
				t.Errorf("Environment = %q, want the trimmed value %q", cfg.Environment, want)
			}
			assertDiags(t, diags, tc.wantDiags)
		})
	}
}

// resolvedValues is the comparable part of a Config: the collaborators
// (DiagWriter, Clock, Jitter) are defaulted references and are excluded.
type resolvedValues struct {
	Enabled     bool
	IngestURL   string
	SourceToken string
	ServiceName string
	Environment string
	MinLevel    slog.Level
	BatchSize   int
	FlushEvery  time.Duration
	QueueSize   int
}

func valuesOf(cfg Config) resolvedValues {
	return resolvedValues{
		Enabled:     cfg.Enabled,
		IngestURL:   cfg.IngestURL,
		SourceToken: cfg.SourceToken,
		ServiceName: cfg.ServiceName,
		Environment: cfg.Environment,
		MinLevel:    cfg.MinLevel,
		BatchSize:   cfg.BatchSize,
		FlushEvery:  cfg.FlushEvery,
		QueueSize:   cfg.QueueSize,
	}
}

// TestResolveConfigIsDeterministic pins the purity of ResolveConfig: the same
// RawConfig yields the same resolved values and the same diagnostics, in the
// same order, on every call (Req 12.1).
func TestResolveConfigIsDeterministic(t *testing.T) {
	raws := []RawConfig{
		{},
		validRaw(),
		{
			SourceToken:  " " + testSourceToken + " ",
			IngestURL:    testIngestURL,
			ServiceName:  "fundis-worker",
			Environment:  "production",
			MinLevel:     "warn",
			BatchSize:    "100",
			FlushSeconds: "10",
			QueueSize:    "2000",
		},
		{
			SourceToken:  testSourceToken,
			IngestURL:    "http://in.logs.example.com/logs",
			ServiceName:  strings.Repeat("s", 65),
			Environment:  "PRODUCTION",
			MinLevel:     "loud",
			BatchSize:    "0",
			FlushSeconds: "301",
			QueueSize:    "nope",
		},
	}

	for i, raw := range raws {
		t.Run(fmt.Sprintf("raw%d", i), func(t *testing.T) {
			firstCfg, firstDiags := ResolveConfig(raw)
			secondCfg, secondDiags := ResolveConfig(raw)

			if valuesOf(firstCfg) != valuesOf(secondCfg) {
				t.Errorf("resolved values differ between calls:\n first: %+v\nsecond: %+v", valuesOf(firstCfg), valuesOf(secondCfg))
			}
			if !reflect.DeepEqual(firstDiags, secondDiags) {
				t.Errorf("diagnostics differ between calls:\n%s\n---\n%s", formatDiags(firstDiags), formatDiags(secondDiags))
			}
		})
	}
}

// TestResolveConfigTestMutatesNoEnvironment enforces the "pure function, no
// t.Setenv" rule for this file itself: validation is exercised through
// RawConfig values only, so a future case cannot quietly reintroduce
// environment mutation (Req 12.1).
func TestResolveConfigTestMutatesNoEnvironment(t *testing.T) {
	src, err := os.ReadFile("config_test.go")
	if err != nil {
		t.Fatalf("read config_test.go: %v", err)
	}

	// Assembled from fragments so that these guards do not match themselves.
	banned := []string{"t." + "Setenv(", "os." + "Setenv(", "os." + "Unsetenv(", "os." + "Getenv("}
	for _, call := range banned {
		if strings.Contains(string(src), call) {
			t.Errorf("config_test.go calls %s; ResolveConfig is pure, so pass values through RawConfig instead", call)
		}
	}
}
