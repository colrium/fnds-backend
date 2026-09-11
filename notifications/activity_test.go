package notifications

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Delivery tests: each Notify* issues one delivery for its event type
// ---------------------------------------------------------------------------

// TestNotifyVacancyCreated_Delivery uses a test server to assert one POST is issued.
func TestNotifyVacancyCreated_Delivery(t *testing.T) {
	callCount := 0
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", ts.URL)
	t.Setenv("SLACK_WEBHOOK_URL", "")
	t.Setenv("ADMIN_DASHBOARD_URL", "https://admin.fundis.co.ke")

	err := NotifyVacancyCreated(context.Background(), VacancyCreatedActivity{
		VacancyID:     "v-123",
		VacancyTitle:  "Plumber",
		CreatorUserID: "u-1",
		CreatorName:   "Amina Otieno",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("NotifyVacancyCreated returned error: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 POST, got %d", callCount)
	}

	// Verify admin links are present in the payload.
	// Note: Go's json encoder escapes < and > as \u003c and \u003e in JSON.
	body := string(gotBody)
	if !strings.Contains(body, "admin.fundis.co.ke/vacancies/v-123|Plumber") {
		t.Fatalf("payload missing vacancy link, got: %s", body)
	}
	if !strings.Contains(body, "admin.fundis.co.ke/users/u-1|Amina Otieno") {
		t.Fatalf("payload missing user link, got: %s", body)
	}
}

// TestNotifyApplicationCreated_Delivery uses a test server to assert one POST is issued.
func TestNotifyApplicationCreated_Delivery(t *testing.T) {
	callCount := 0
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", ts.URL)
	t.Setenv("SLACK_WEBHOOK_URL", "")
	t.Setenv("ADMIN_DASHBOARD_URL", "https://admin.fundis.co.ke")

	err := NotifyApplicationCreated(context.Background(), ApplicationActivity{
		ApplicantName:  "Jane Doe",
		ApplicantEmail: "jane@example.com",
		ApplicantPhone: "+254700000000",
		ApplicantID:    "u-42",
		VacancyID:      "v-99",
		VacancyTitle:   "Electrician",
		AppliedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("NotifyApplicationCreated returned error: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 POST, got %d", callCount)
	}

	// Verify admin links are present in the payload.
	// Note: Go's json encoder escapes < and > as \u003c and \u003e in JSON.
	body := string(gotBody)
	if !strings.Contains(body, "admin.fundis.co.ke/users/u-42|Jane Doe") {
		t.Fatalf("payload missing applicant link, got: %s", body)
	}
	if !strings.Contains(body, "admin.fundis.co.ke/vacancies/v-99|Electrician") {
		t.Fatalf("payload missing vacancy link, got: %s", body)
	}
}

// TestNotifyUserRegistered_Delivery uses a test server to assert one POST is issued.
func TestNotifyUserRegistered_Delivery(t *testing.T) {
	callCount := 0
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", ts.URL)
	t.Setenv("SLACK_WEBHOOK_URL", "")
	t.Setenv("ADMIN_DASHBOARD_URL", "https://admin.fundis.co.ke")

	err := NotifyUserRegistered(context.Background(), RegistrationActivity{
		UserName:     "John Smith",
		UserEmail:    "john@example.com",
		UserPhone:    "+254711111111",
		UserID:       "u-77",
		UserType:     "artisan",
		RegisteredAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("NotifyUserRegistered returned error: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 POST, got %d", callCount)
	}

	// Verify admin link is present in the payload.
	// Note: Go's json encoder escapes < and > as \u003c and \u003e in JSON.
	body := string(gotBody)
	if !strings.Contains(body, "admin.fundis.co.ke/users/u-77|John Smith") {
		t.Fatalf("payload missing user link, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Silent skip: unconfigured webhook returns no error
// ---------------------------------------------------------------------------

// TestNotifyUnconfiguredWebhook_SilentSkip asserts no error when both vars empty.
func TestNotifyUnconfiguredWebhook_SilentSkip(t *testing.T) {
	t.Setenv("SLACK_OPS_NOTIFS_URL", "")
	t.Setenv("SLACK_WEBHOOK_URL", "")

	err := NotifyVacancyCreated(context.Background(), VacancyCreatedActivity{
		VacancyID:     "v-1",
		VacancyTitle:  "Test",
		CreatorUserID: "u-1",
		CreatorName:   "Amina Otieno",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("expected nil error (silent skip), got: %v", err)
	}
}

func TestNotifyApplicationUnconfiguredWebhook_SilentSkip(t *testing.T) {
	t.Setenv("SLACK_OPS_NOTIFS_URL", "")
	t.Setenv("SLACK_WEBHOOK_URL", "")

	err := NotifyApplicationCreated(context.Background(), ApplicationActivity{
		ApplicantName:  "Alice",
		ApplicantEmail: "alice@example.com",
		ApplicantPhone: "+254700000000",
		ApplicantID:    "u-1",
		VacancyID:      "v-1",
		VacancyTitle:   "Test",
		AppliedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("expected nil error (silent skip), got: %v", err)
	}
}

func TestNotifyRegistrationUnconfiguredWebhook_SilentSkip(t *testing.T) {
	t.Setenv("SLACK_OPS_NOTIFS_URL", "")
	t.Setenv("SLACK_WEBHOOK_URL", "")

	err := NotifyUserRegistered(context.Background(), RegistrationActivity{
		UserName:     "Bob",
		UserEmail:    "bob@example.com",
		UserPhone:    "+254722222222",
		UserID:       "u-2",
		RegisteredAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("expected nil error (silent skip), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Property 7: Failure logs exclude PII
// **Validates: Requirements 6.5**
//
// Capture slog output on a forced delivery failure and assert none of the
// name/email/phone values appear in the log output.
// ---------------------------------------------------------------------------

func TestProperty7_FailureLogsExcludePII(t *testing.T) {
	// Create a test server that always returns 500 to force a delivery failure.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := io.WriteString(w, "internal server error"); err != nil {
			t.Errorf("writing test response: %v", err)
		}
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", ts.URL)
	t.Setenv("SLACK_WEBHOOK_URL", "")

	// PII values that must NOT appear in logs.
	piiValues := []string{
		"Sensitive Name",
		"sensitive@secret.com",
		"+254799999999",
	}

	// Capture slog output.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldLogger)

	// Test with ApplicationActivity (has most PII fields).
	_ = NotifyApplicationCreated(context.Background(), ApplicationActivity{
		ApplicantName:  piiValues[0],
		ApplicantEmail: piiValues[1],
		ApplicantPhone: piiValues[2],
		ApplicantID:    "u-secret-id",
		VacancyID:      "v-123",
		VacancyTitle:   "Plumber",
		AppliedAt:      time.Now(),
	})

	logOutput := buf.String()
	for _, pii := range piiValues {
		if strings.Contains(logOutput, pii) {
			t.Fatalf("log output contains PII value %q:\n%s", pii, logOutput)
		}
	}

	// Reset buffer and test with RegistrationActivity.
	buf.Reset()

	regPII := []string{
		"Another Secret Name",
		"another@secret.org",
		"+254788888888",
	}

	_ = NotifyUserRegistered(context.Background(), RegistrationActivity{
		UserName:     regPII[0],
		UserEmail:    regPII[1],
		UserPhone:    regPII[2],
		UserID:       "u-reg-id",
		RegisteredAt: time.Now(),
	})

	logOutput = buf.String()
	for _, pii := range regPII {
		if strings.Contains(logOutput, pii) {
			t.Fatalf("log output contains PII value %q:\n%s", pii, logOutput)
		}
	}
}

// ---------------------------------------------------------------------------
// Property 8: OpsNotifs channel type falls back to SLACK_WEBHOOK_URL
// **Validates: Requirements 6.1, 6.6**
//
// Set SLACK_OPS_NOTIFS_URL="" and SLACK_WEBHOOK_URL="https://fallback",
// assert delivery targets the fallback URL.
// ---------------------------------------------------------------------------

func TestProperty8_OpsNotifsFallsBackToSlackWebhookURL(t *testing.T) {
	var gotURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = "http://" + r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("SLACK_OPS_NOTIFS_URL", "")
	t.Setenv("SLACK_WEBHOOK_URL", ts.URL)

	err := NotifyVacancyCreated(context.Background(), VacancyCreatedActivity{
		VacancyID:     "v-fallback",
		VacancyTitle:  "Fallback Test",
		CreatorUserID: "u-1",
		CreatorName:   "Amina Otieno",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("NotifyVacancyCreated returned error: %v", err)
	}

	// The test server received the request, so the fallback URL was used.
	if gotURL == "" {
		t.Fatal("no request was made — fallback URL was not used")
	}

	// Also verify via resolveWebhookURL directly.
	resolved := resolveWebhookURL(SlackChannelOps)
	if resolved != ts.URL {
		t.Fatalf("resolveWebhookURL(SlackChannelOps) = %q, want %q", resolved, ts.URL)
	}
}

// ---------------------------------------------------------------------------
// Property 9: Channel type routing resolves the correct URL
// **Validates: Requirements 6.1**
//
// For all combinations of configured/unconfigured env vars and channel types,
// assert resolveWebhookURL returns the expected URL per the precedence rules.
// ---------------------------------------------------------------------------

func TestProperty9_ChannelTypeRoutingResolvesCorrectURL(t *testing.T) {
	tests := []struct {
		name        string
		opsURL      string
		webhookURL  string
		channelType SlackChannelType
		wantURL     string
	}{
		{
			name:        "OpsNotifs: both configured → prefer SLACK_OPS_NOTIFS_URL",
			opsURL:      "https://ops.example.com/hook",
			webhookURL:  "https://fallback.example.com/hook",
			channelType: SlackChannelOps,
			wantURL:     "https://ops.example.com/hook",
		},
		{
			name:        "OpsNotifs: ops empty, webhook configured → fallback to SLACK_WEBHOOK_URL",
			opsURL:      "",
			webhookURL:  "https://fallback.example.com/hook",
			channelType: SlackChannelOps,
			wantURL:     "https://fallback.example.com/hook",
		},
		{
			name:        "OpsNotifs: both empty → empty string (triggers skip)",
			opsURL:      "",
			webhookURL:  "",
			channelType: SlackChannelOps,
			wantURL:     "",
		},
		{
			name:        "OpsNotifs: ops configured, webhook empty → use ops URL",
			opsURL:      "https://ops-only.example.com/hook",
			webhookURL:  "",
			channelType: SlackChannelOps,
			wantURL:     "https://ops-only.example.com/hook",
		},
		{
			name:        "Unknown channel type: both configured → fallback to SLACK_WEBHOOK_URL",
			opsURL:      "https://ops.example.com/hook",
			webhookURL:  "https://fallback.example.com/hook",
			channelType: SlackChannelType("unknown_channel"),
			wantURL:     "https://fallback.example.com/hook",
		},
		{
			name:        "Unknown channel type: webhook empty → empty string",
			opsURL:      "https://ops.example.com/hook",
			webhookURL:  "",
			channelType: SlackChannelType("unknown_channel"),
			wantURL:     "",
		},
		{
			name:        "Unknown channel type: both empty → empty string",
			opsURL:      "",
			webhookURL:  "",
			channelType: SlackChannelType("unknown_channel"),
			wantURL:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SLACK_OPS_NOTIFS_URL", tc.opsURL)
			t.Setenv("SLACK_WEBHOOK_URL", tc.webhookURL)

			got := resolveWebhookURL(tc.channelType)
			if got != tc.wantURL {
				t.Fatalf("resolveWebhookURL(%q) = %q, want %q", tc.channelType, got, tc.wantURL)
			}
		})
	}
}
