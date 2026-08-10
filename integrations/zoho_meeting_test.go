package integrations

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestService builds a service pointed at a stub server, bypassing the env so
// tests never depend on real credentials.
func newTestService(t *testing.T, baseURL string) *ZohoMeetingService {
	t.Helper()
	location, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		t.Fatalf("loading test location: %v", err)
	}
	return &ZohoMeetingService{
		clientID:        "test-client",
		clientSecret:    "test-secret",
		refreshToken:    "test-refresh",
		zsoid:           "12345",
		presenterID:     999,
		accountsBaseURL: baseURL,
		meetingBaseURL:  baseURL,
		location:        location,
		httpClient:      &http.Client{Timeout: 5 * time.Second},
	}
}

// zohoStub serves both the OAuth and session endpoints and records what it saw.
type zohoStub struct {
	server        *httptest.Server
	refreshCalls  atomic.Int32
	sessionCalls  atomic.Int32
	lastSession   map[string]any
	lastOAuthForm string
	expiresIn     int64
	// sessionStatus lets a test force a 401 on the first call only.
	firstCallStatus int
}

func newZohoStub(t *testing.T) *zohoStub {
	t.Helper()
	stub := &zohoStub{expiresIn: 3600}
	mux := http.NewServeMux()

	mux.HandleFunc("/oauth/v2/token", func(w http.ResponseWriter, r *http.Request) {
		stub.refreshCalls.Add(1)
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		stub.lastOAuthForm = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-token-abc",
			"expires_in":   stub.expiresIn,
		})
	})

	mux.HandleFunc("/api/v2/12345/sessions.json", func(w http.ResponseWriter, r *http.Request) {
		call := stub.sessionCalls.Add(1)
		if call == 1 && stub.firstCallStatus != 0 {
			w.WriteHeader(stub.firstCallStatus)
			return
		}
		var payload struct {
			Session map[string]any `json:"session"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.lastSession = payload.Session
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{
				"meetingKey": 887766,
				"joinLink":   "https://meeting.zoho.com/join/887766",
				"startLink":  "https://meeting.zoho.com/start/887766",
			},
		})
	})

	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	return stub
}

// The bug this guards: the start time was formatted from the UTC instant while
// the payload declared Africa/Nairobi, so Zoho scheduled every meeting three
// hours early. The wall clock sent must match the declared zone.
func TestCreateMeetingSendsStartTimeInDeclaredTimezone(t *testing.T) {
	stub := newZohoStub(t)
	svc := newTestService(t, stub.server.URL)

	// 09:00 UTC is 12:00 in Nairobi (UTC+3).
	slot := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	if _, err := svc.CreateMeeting("Oral Assessment", slot, 60); err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	gotTime, _ := stub.lastSession["startTime"].(string)
	if want := "Aug 12, 2026 12:00 PM"; gotTime != want {
		t.Errorf("startTime = %q, want %q (the Nairobi wall clock for 09:00 UTC)", gotTime, want)
	}
	if gotZone, _ := stub.lastSession["timezone"].(string); gotZone != "Africa/Nairobi" {
		t.Errorf("timezone = %q, want Africa/Nairobi", gotZone)
	}
}

// A start time already in another zone must still be converted, not reformatted.
func TestCreateMeetingConvertsNonUTCStartTime(t *testing.T) {
	stub := newZohoStub(t)
	svc := newTestService(t, stub.server.URL)

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 02:00 New York (EDT, UTC-4) == 06:00 UTC == 09:00 Nairobi.
	slot := time.Date(2026, 8, 12, 2, 0, 0, 0, newYork)
	if _, err := svc.CreateMeeting("Oral Assessment", slot, 60); err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	gotTime, _ := stub.lastSession["startTime"].(string)
	if want := "Aug 12, 2026 09:00 AM"; gotTime != want {
		t.Errorf("startTime = %q, want %q", gotTime, want)
	}
}

func TestCreateMeetingSendsDurationInMilliseconds(t *testing.T) {
	stub := newZohoStub(t)
	svc := newTestService(t, stub.server.URL)

	if _, err := svc.CreateMeeting("Oral", time.Now().UTC(), 45); err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}
	got, _ := stub.lastSession["duration"].(float64)
	if want := float64(45 * 60 * 1000); got != want {
		t.Errorf("duration = %v, want %v", got, want)
	}
}

// A slot whose end is not after its start must not produce a zero-length meeting.
func TestCreateMeetingFallsBackToDefaultDuration(t *testing.T) {
	stub := newZohoStub(t)
	svc := newTestService(t, stub.server.URL)

	if _, err := svc.CreateMeeting("Oral", time.Now().UTC(), 0); err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}
	got, _ := stub.lastSession["duration"].(float64)
	if want := float64(defaultMeetingDurationMin * 60 * 1000); got != want {
		t.Errorf("duration = %v, want the %d-minute default (%v)", got, defaultMeetingDurationMin, want)
	}
}

// The cache is the reason the service is a startup singleton: a second call must
// not trigger a second OAuth refresh.
func TestAccessTokenIsCachedAcrossCalls(t *testing.T) {
	stub := newZohoStub(t)
	svc := newTestService(t, stub.server.URL)

	for i := 0; i < 3; i++ {
		if _, err := svc.CreateMeeting("Oral", time.Now().UTC(), 60); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := stub.refreshCalls.Load(); got != 1 {
		t.Errorf("OAuth refreshes = %d, want 1 (token should be cached)", got)
	}
	if got := stub.sessionCalls.Load(); got != 3 {
		t.Errorf("session calls = %d, want 3", got)
	}
}

// A 401 means the cached token was revoked early; exactly one retry is expected.
func TestCreateMeetingRetriesOnceAfterUnauthorized(t *testing.T) {
	stub := newZohoStub(t)
	stub.firstCallStatus = http.StatusUnauthorized
	svc := newTestService(t, stub.server.URL)

	if _, err := svc.CreateMeeting("Oral", time.Now().UTC(), 60); err != nil {
		t.Fatalf("CreateMeeting should have recovered after a 401: %v", err)
	}
	if got := stub.sessionCalls.Load(); got != 2 {
		t.Errorf("session calls = %d, want 2 (original + one retry)", got)
	}
	if got := stub.refreshCalls.Load(); got != 2 {
		t.Errorf("OAuth refreshes = %d, want 2 (initial + post-401)", got)
	}
}

// A non-auth failure must not be retried: retrying a bad request just doubles
// the load and the latency.
func TestCreateMeetingDoesNotRetryOnServerError(t *testing.T) {
	stub := newZohoStub(t)
	stub.firstCallStatus = http.StatusInternalServerError
	svc := newTestService(t, stub.server.URL)

	if _, err := svc.CreateMeeting("Oral", time.Now().UTC(), 60); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if got := stub.sessionCalls.Load(); got != 1 {
		t.Errorf("session calls = %d, want 1 (no retry on 500)", got)
	}
}

// Credentials must travel in the POST body, never the query string, because Go
// puts the full URL in transport errors and those errors get logged.
func TestRefreshSendsCredentialsInBodyNotQuery(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	}))
	defer server.Close()

	svc := newTestService(t, server.URL)
	if _, _, err := svc.refreshAccessToken(); err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query string = %q, want empty (credentials belong in the body)", gotQuery)
	}
}

// Second layer of the same protection: even a URL that does carry a query must
// be scrubbed before the error is wrapped for logging.
func TestRedactZohoErrorStripsQueryString(t *testing.T) {
	svc := newTestService(t, "https://accounts.zoho.invalid")
	svc.accountsBaseURL = "https://accounts.zoho.invalid"

	// Force a transport failure against an unresolvable host.
	_, _, err := svc.refreshAccessToken()
	if err == nil {
		t.Fatal("expected a transport error")
	}
	for _, secret := range []string{"test-secret", "test-refresh"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked %q: %s", secret, err)
		}
	}
}

func TestRedactZohoErrorRedactsQueryWhenPresent(t *testing.T) {
	original := &url.Error{
		Op:  "Post",
		URL: "https://accounts.zoho.com/oauth/v2/token?client_secret=SHHH&grant_type=refresh_token",
		Err: errors.New("dial tcp: no such host"),
	}
	redacted := redactZohoError(original)
	if strings.Contains(redacted.Error(), "SHHH") {
		t.Errorf("secret survived redaction: %s", redacted)
	}
	if !strings.Contains(redacted.Error(), "[redacted]") {
		t.Errorf("expected a [redacted] marker, got: %s", redacted)
	}
}

// A response without a joinLink is useless to the artisan and must be an error
// rather than an appointment with a blank link.
func TestCreateMeetingRejectsResponseWithoutJoinLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/token") {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"meetingKey": 1}})
	}))
	defer server.Close()

	svc := newTestService(t, server.URL)
	if _, err := svc.CreateMeeting("Oral", time.Now().UTC(), 60); err == nil {
		t.Fatal("expected an error when the response carries no joinLink")
	}
}

// Zoho reports OAuth failures as a JSON error field, sometimes with a 200.
func TestRefreshSurfacesOAuthErrorField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
	}))
	defer server.Close()

	svc := newTestService(t, server.URL)
	_, _, err := svc.refreshAccessToken()
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("err = %v, want it to surface invalid_client", err)
	}
}

// An implausible expires_in would make the 60-second safety margin produce an
// already-expired cache entry, refreshing on every single call.
func TestRefreshRejectsImplausibleExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 30})
	}))
	defer server.Close()

	svc := newTestService(t, server.URL)
	if _, _, err := svc.refreshAccessToken(); err == nil {
		t.Fatal("expected an error for expires_in below the safety margin")
	}
}

func TestNewZohoMeetingServiceRequiresEveryCredential(t *testing.T) {
	full := map[string]string{
		"ZOHO_MEETING_CLIENT_ID":     "cid",
		"ZOHO_MEETING_CLIENT_SECRET": "secret",
		"ZOHO_MEETING_REFRESH_TOKEN": "refresh",
		"ZOHO_MEETING_ZSOID":         "12345",
		"ZOHO_MEETING_PRESENTER_ID":  "999",
	}

	// Every single omission must be rejected, so a partial config cannot pass
	// startup and then fail on each booking.
	for omitted := range full {
		t.Run("missing_"+omitted, func(t *testing.T) {
			for k, v := range full {
				if k == omitted {
					t.Setenv(k, "")
					continue
				}
				t.Setenv(k, v)
			}
			_, err := NewZohoMeetingService()
			if !errors.Is(err, ErrZohoNotConfigured) {
				t.Fatalf("err = %v, want ErrZohoNotConfigured", err)
			}
			if !strings.Contains(err.Error(), omitted) {
				t.Errorf("error should name the missing var %q, got: %v", omitted, err)
			}
			// The error is logged, so it must not echo the values.
			for _, secret := range []string{"secret", "refresh"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaked a credential value %q: %v", secret, err)
				}
			}
		})
	}

	t.Run("fully_configured", func(t *testing.T) {
		for k, v := range full {
			t.Setenv(k, v)
		}
		svc, err := NewZohoMeetingService()
		if err != nil {
			t.Fatalf("NewZohoMeetingService: %v", err)
		}
		if svc.location.String() != "Africa/Nairobi" {
			t.Errorf("default timezone = %q, want Africa/Nairobi", svc.location)
		}
		if svc.accountsBaseURL != "https://accounts.zoho.com" {
			t.Errorf("accountsBaseURL = %q, want the .com default", svc.accountsBaseURL)
		}
	})

	t.Run("honours_datacenter_and_timezone_overrides", func(t *testing.T) {
		for k, v := range full {
			t.Setenv(k, v)
		}
		t.Setenv("ZOHO_ACCOUNTS_BASE_URL", "https://accounts.zoho.eu/")
		t.Setenv("ZOHO_MEETING_BASE_URL", "https://meeting.zoho.eu")
		t.Setenv("ZOHO_MEETING_TIMEZONE", "Europe/Berlin")

		svc, err := NewZohoMeetingService()
		if err != nil {
			t.Fatalf("NewZohoMeetingService: %v", err)
		}
		// The trailing slash must be trimmed or every path becomes a double slash.
		if svc.accountsBaseURL != "https://accounts.zoho.eu" {
			t.Errorf("accountsBaseURL = %q, want the trailing slash trimmed", svc.accountsBaseURL)
		}
		if svc.location.String() != "Europe/Berlin" {
			t.Errorf("timezone = %q, want Europe/Berlin", svc.location)
		}
	})

	t.Run("rejects_invalid_timezone", func(t *testing.T) {
		for k, v := range full {
			t.Setenv(k, v)
		}
		t.Setenv("ZOHO_MEETING_TIMEZONE", "Not/AZone")
		if _, err := NewZohoMeetingService(); err == nil {
			t.Fatal("expected an error for an unparseable timezone")
		}
	})
}
