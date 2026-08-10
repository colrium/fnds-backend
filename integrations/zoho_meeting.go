package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"backend/configs"
)

// zohoStartTimeLayout is the "MMM dd, yyyy hh:mm a" format the Meeting API
// expects. The formatted wall clock is interpreted in the timezone sent
// alongside it, so the time must be converted into that zone before formatting.
const zohoStartTimeLayout = "Jan 02, 2006 03:04 PM"

// defaultMeetingDurationMin is used when a slot yields a non-positive duration,
// so a malformed slot still produces a usable meeting rather than an API error.
const defaultMeetingDurationMin = 60

// ErrZohoNotConfigured is returned by NewZohoMeetingService when any required
// credential is missing. Callers treat it as "video calls are unavailable" and
// continue, since a booking is still valid without a meeting link.
var ErrZohoNotConfigured = errors.New("zoho meeting: not configured")

// ZohoMeetingService manages Zoho Meeting sessions for oral assessments.
//
// It must be built once at startup and shared: the access-token cache lives on
// the instance, so a per-request instance would force an OAuth refresh on every
// call and risk Zoho's refresh rate limit. All exported methods are safe for
// concurrent use.
type ZohoMeetingService struct {
	clientID     string
	clientSecret string
	refreshToken string
	zsoid        string
	presenterID  int64

	accountsBaseURL string
	meetingBaseURL  string
	location        *time.Location
	httpClient      *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// ZohoMeetingResult is the data returned after creating a meeting.
type ZohoMeetingResult struct {
	MeetingKey string `json:"meeting_key"`
	JoinLink   string `json:"join_link"`
	StartLink  string `json:"start_link"`
}

// NewZohoMeetingService builds the service from the environment.
//
// It returns ErrZohoNotConfigured unless every credential is present. Checking
// all of them up front means a partial configuration fails once at startup
// instead of failing every booking at runtime.
func NewZohoMeetingService() (*ZohoMeetingService, error) {
	var missing []string
	clientID := configs.ZohoMeetingClientID()
	clientSecret := configs.ZohoMeetingClientSecret()
	refreshToken := configs.ZohoMeetingRefreshToken()
	zsoid := configs.ZohoMeetingZSOID()
	presenterID := configs.ZohoMeetingPresenterID()

	for name, value := range map[string]string{
		"ZOHO_MEETING_CLIENT_ID":     clientID,
		"ZOHO_MEETING_CLIENT_SECRET": clientSecret,
		"ZOHO_MEETING_REFRESH_TOKEN": refreshToken,
		"ZOHO_MEETING_ZSOID":         zsoid,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if presenterID == 0 {
		missing = append(missing, "ZOHO_MEETING_PRESENTER_ID")
	}
	if len(missing) > 0 {
		// Names only: the values are secrets and this error is logged.
		return nil, fmt.Errorf("%w (missing: %s)", ErrZohoNotConfigured, strings.Join(missing, ", "))
	}

	tzName := configs.ZohoMeetingTimezone()
	location, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: invalid ZOHO_MEETING_TIMEZONE %q: %w", tzName, err)
	}

	return &ZohoMeetingService{
		clientID:        clientID,
		clientSecret:    clientSecret,
		refreshToken:    refreshToken,
		zsoid:           zsoid,
		presenterID:     presenterID,
		accountsBaseURL: configs.ZohoAccountsBaseURL(),
		meetingBaseURL:  configs.ZohoMeetingBaseURL(),
		location:        location,
		httpClient:      &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// CreateMeeting creates a Zoho Meeting session and returns the meeting key,
// join link (for the artisan), and start link (for the assessor/presenter).
//
// startTime may be in any location; it is converted to the service timezone
// before formatting so the wall clock sent to Zoho matches the zone declared
// with it.
func (s *ZohoMeetingService) CreateMeeting(topic string, startTime time.Time, durationMin int) (*ZohoMeetingResult, error) {
	token, err := s.getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: obtaining access token: %w", err)
	}

	result, err := s.createMeetingWithToken(token, topic, startTime, durationMin)
	if err == nil {
		return result, nil
	}
	// Retry once, and only for an expired token: the cached token can be revoked
	// by Zoho before its stated expiry. Any other failure is returned as-is so a
	// bad request is not retried pointlessly.
	if !errors.Is(err, errZohoUnauthorized) {
		return nil, err
	}
	s.invalidateToken()
	token, err = s.getAccessToken()
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: refreshing access token: %w", err)
	}
	return s.createMeetingWithToken(token, topic, startTime, durationMin)
}

// errZohoUnauthorized marks a 401 so CreateMeeting can distinguish "token stale,
// worth retrying" from a permanent failure.
var errZohoUnauthorized = errors.New("zoho meeting: unauthorized")

func (s *ZohoMeetingService) createMeetingWithToken(token, topic string, startTime time.Time, durationMin int) (*ZohoMeetingResult, error) {
	endpoint := fmt.Sprintf("%s/api/v2/%s/sessions.json", s.meetingBaseURL, s.zsoid)

	if durationMin <= 0 {
		durationMin = defaultMeetingDurationMin
	}

	body := map[string]any{
		"session": map[string]any{
			"topic":     topic,
			"presenter": s.presenterID,
			"startTime": startTime.In(s.location).Format(zohoStartTimeLayout),
			// Zoho expects the duration in milliseconds.
			"duration": int64(durationMin) * 60 * 1000,
			"timezone": s.location.String(),
		},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: encoding request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Zoho-oauthtoken "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: request failed: %w", redactZohoError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("zoho meeting: reading response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errZohoUnauthorized
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("zoho meeting: unexpected status %d: %s", resp.StatusCode, truncate(string(respBody), 512))
	}

	var parsed struct {
		Session struct {
			MeetingKey int64  `json:"meetingKey"`
			JoinLink   string `json:"joinLink"`
			StartLink  string `json:"startLink"`
		} `json:"session"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("zoho meeting: parsing response: %w", err)
	}
	if parsed.Session.JoinLink == "" {
		return nil, fmt.Errorf("zoho meeting: response contained no joinLink")
	}

	return &ZohoMeetingResult{
		MeetingKey: fmt.Sprintf("%d", parsed.Session.MeetingKey),
		JoinLink:   parsed.Session.JoinLink,
		StartLink:  parsed.Session.StartLink,
	}, nil
}

// getAccessToken returns a cached token or refreshes it from Zoho OAuth2.
func (s *ZohoMeetingService) getAccessToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accessToken != "" && time.Now().Before(s.tokenExpiry) {
		return s.accessToken, nil
	}

	token, expiresIn, err := s.refreshAccessToken()
	if err != nil {
		return "", err
	}
	s.accessToken = token
	// Expire 60 seconds early to avoid using a token that lapses in flight.
	s.tokenExpiry = time.Now().Add(time.Duration(expiresIn-60) * time.Second)
	return s.accessToken, nil
}

func (s *ZohoMeetingService) invalidateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessToken = ""
	s.tokenExpiry = time.Time{}
}

// refreshAccessToken exchanges the refresh token for a new access token.
//
// The credentials go in the POST form body, never the query string: Go embeds
// the full URL in transport errors (*url.Error), and those errors are logged, so
// a query-string secret would end up in the application logs on any network
// blip. redactZohoError is a second layer for the same reason.
func (s *ZohoMeetingService) refreshAccessToken() (string, int64, error) {
	form := url.Values{}
	form.Set("refresh_token", s.refreshToken)
	form.Set("client_id", s.clientID)
	form.Set("client_secret", s.clientSecret)
	form.Set("grant_type", "refresh_token")

	endpoint := s.accountsBaseURL + "/oauth/v2/token"
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("zoho oauth: creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("zoho oauth: request failed: %w", redactZohoError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("zoho oauth: reading response: %w", err)
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}
	// Parse before checking the status: Zoho reports OAuth failures as a JSON
	// "error" field, sometimes with a 200.
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("zoho oauth: parsing response (status %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != "" {
		return "", 0, fmt.Errorf("zoho oauth: %s", parsed.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("zoho oauth: unexpected status %d", resp.StatusCode)
	}
	if parsed.AccessToken == "" {
		return "", 0, fmt.Errorf("zoho oauth: empty access_token in response")
	}
	if parsed.ExpiresIn <= 60 {
		// Guard the 60-second safety margin from producing an already-expired
		// cache entry, which would refresh on every single call.
		return "", 0, fmt.Errorf("zoho oauth: implausible expires_in %d", parsed.ExpiresIn)
	}

	return parsed.AccessToken, parsed.ExpiresIn, nil
}

// redactZohoError strips the query string from URLs inside *url.Error so a
// credential can never reach a log line, even if a future caller reintroduces
// query-string parameters.
func redactZohoError(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil && parsed.RawQuery != "" {
		parsed.RawQuery = "[redacted]"
		return fmt.Errorf("%s %q: %w", urlErr.Op, parsed.String(), urlErr.Err)
	}
	return err
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
