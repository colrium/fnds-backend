package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BlockKitMessage is the top-level Slack webhook payload.
type BlockKitMessage struct {
	Text   string  `json:"text,omitempty"`
	Blocks []Block `json:"blocks"`
}

// Block represents a single Slack Block Kit block.
type Block struct {
	Type     string       `json:"type"`               // "header" | "section" | "context"
	Text     *TextObject  `json:"text,omitempty"`     // header, section
	Fields   []TextObject `json:"fields,omitempty"`   // section with field columns
	Elements []TextObject `json:"elements,omitempty"` // context
}

// TextObject is a Slack text composition object.
type TextObject struct {
	Type string `json:"type"` // "plain_text" | "mrkdwn"
	Text string `json:"text"`
}

// MessageField is the input type for building activity messages.
type MessageField struct {
	Label string
	Value string
	URL   string
}

// buildActivityMessage renders a header block with the title, then a section
// block of field columns. Fields with a blank (trimmed) Value are skipped.
// If all fields are blank, no section block is emitted.
func buildActivityMessage(title string, fields []MessageField) BlockKitMessage {
	header := Block{
		Type: "header",
		Text: &TextObject{Type: "plain_text", Text: clipSlackText(title, 150)},
	}

	columns := make([]TextObject, 0, len(fields))
	for _, f := range fields {
		if strings.TrimSpace(f.Value) == "" {
			continue
		}
		value := SlackText(clipSlackText(f.Value, 250))
		if f.URL != "" {
			value = slackNamedLink(f.URL, f.Value)
		}
		columns = append(columns, TextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*%s:*\n%s", SlackText(clipSlackText(f.Label, 60)), value),
		})
	}

	blocks := []Block{header}
	for len(columns) > 0 {
		n := min(len(columns), 10)
		blocks = append(blocks, Block{Type: "section", Fields: columns[:n]})
		columns = columns[n:]
	}
	return BlockKitMessage{Blocks: blocks}
}

// ErrSlackWebhookNotConfigured is returned when Send is called with a blank webhook URL.
var ErrSlackWebhookNotConfigured = errors.New("slack activity: webhook url not configured")

// SlackActivityClient posts Block Kit messages to a Slack incoming webhook.
type SlackActivityClient struct {
	webhookURL string
	baseURL    string
	httpClient *http.Client
}

// SlackActivityOption configures a SlackActivityClient.
type SlackActivityOption func(*SlackActivityClient)

// WithSlackActivityHTTPClient overrides the default HTTP client.
func WithSlackActivityHTTPClient(httpClient *http.Client) SlackActivityOption {
	return func(c *SlackActivityClient) { c.httpClient = httpClient }
}

// WithSlackActivityBaseURL overrides the endpoint URL (trailing slash is trimmed).
func WithSlackActivityBaseURL(baseURL string) SlackActivityOption {
	return func(c *SlackActivityClient) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// NewSlackActivityClient creates a SlackActivityClient with sensible defaults.
func NewSlackActivityClient(webhookURL string, options ...SlackActivityOption) *SlackActivityClient {
	client := &SlackActivityClient{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
	for _, option := range options {
		option(client)
	}
	return client
}

// Send posts a BlockKitMessage to the configured Slack webhook.
func (c *SlackActivityClient) Send(ctx context.Context, msg BlockKitMessage) error {
	if strings.TrimSpace(c.webhookURL) == "" {
		return ErrSlackWebhookNotConfigured
	}

	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(msg); err != nil {
		return fmt.Errorf("slack activity: encoding request: %w", err)
	}

	endpoint := c.webhookURL
	if c.baseURL != "" {
		endpoint = c.baseURL
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("slack activity: creating request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("slack activity: sending request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf(
		"slack activity: provider returned http %d: %s",
		response.StatusCode,
		strings.TrimSpace(string(responseBody)),
	)
}

// VacancyCreatedEvent carries vacancy-creation fields for building the notification.
type VacancyCreatedEvent struct {
	VacancyID     string
	VacancyTitle  string
	CreatorUserID string
	CreatorName   string
	OwnerUserID   string
	OwnerName     string
	CompanyName   string
	CreatedAt     time.Time
	AdminBaseURL  string
	Environment   string
}

type ApplicationEvent struct {
	ApplicantName  string
	ApplicantEmail string
	ApplicantPhone string
	ApplicantID    string
	VacancyID      string
	VacancyTitle   string
	AppliedAt      time.Time
	AdminBaseURL   string
	Environment    string
}

type RegistrationEvent struct {
	UserName     string
	UserEmail    string
	UserPhone    string
	UserID       string
	UserType     string
	RegisteredAt time.Time
	AdminBaseURL string
	Environment  string
}

type ProfileSubmittedEvent struct {
	UserID       string
	UserName     string
	UserEmail    string
	SubmittedAt  time.Time
	AdminBaseURL string
	Environment  string
}

func BuildVacancyCreatedMessage(e VacancyCreatedEvent) BlockKitMessage {
	vacancyURL := DashboardRecordURL(e.AdminBaseURL, "vacancies", e.VacancyID)
	fields := []MessageField{
		{Label: "Vacancy", Value: DisplayName(e.VacancyTitle, "", "Untitled vacancy"), URL: vacancyURL},
		{Label: "Posted by", Value: DisplayName(e.CreatorName, "", "View account"), URL: DashboardRecordURL(e.AdminBaseURL, "users", e.CreatorUserID)},
		{Label: "Company", Value: e.CompanyName, URL: vacancyURL},
	}
	if e.OwnerUserID != "" && e.OwnerUserID != e.CreatorUserID {
		fields = append(fields, MessageField{Label: "On behalf of", Value: DisplayName(e.OwnerName, "", "View employer"), URL: DashboardRecordURL(e.AdminBaseURL, "users", e.OwnerUserID)})
	}
	return BuildDashboardMessage(DashboardMessage{
		Title: "💼 New vacancy", Summary: "A new opportunity is ready to view in the admin dashboard.",
		Fields:     fields,
		Actions:    []DashboardAction{{Label: "View vacancy", URL: vacancyURL}},
		OccurredAt: e.CreatedAt, Environment: e.Environment,
	})
}

func BuildApplicationMessage(e ApplicationEvent) BlockKitMessage {
	vacancyURL := DashboardRecordURL(e.AdminBaseURL, "vacancies", e.VacancyID)
	applicantURL := DashboardRecordURL(e.AdminBaseURL, "users", e.ApplicantID)
	return BuildDashboardMessage(DashboardMessage{
		Title: "📩 New application", Summary: "An artisan has applied. Open the vacancy to view their application.",
		Fields: []MessageField{
			{Label: "Applicant", Value: DisplayName(e.ApplicantName, e.ApplicantEmail, "View applicant"), URL: applicantURL},
			{Label: "Vacancy", Value: DisplayName(e.VacancyTitle, "", "View vacancy"), URL: vacancyURL},
			{Label: "Email", Value: e.ApplicantEmail},
			{Label: "Phone", Value: e.ApplicantPhone},
		},
		Actions:    []DashboardAction{{Label: "View applications", URL: vacancyURL}, {Label: "View applicant", URL: applicantURL}},
		OccurredAt: e.AppliedAt, Environment: e.Environment,
	})
}

func BuildRegistrationMessage(e RegistrationEvent) BlockKitMessage {
	userURL := DashboardRecordURL(e.AdminBaseURL, "users", e.UserID)
	return BuildDashboardMessage(DashboardMessage{
		Title: "👋 New registration", Summary: "A new account has joined Fundis.",
		Fields: []MessageField{
			{Label: "Person", Value: DisplayName(e.UserName, e.UserEmail, "View account"), URL: userURL},
			{Label: "Account type", Value: strings.ReplaceAll(e.UserType, "_", " ")},
			{Label: "Email", Value: e.UserEmail},
			{Label: "Phone", Value: e.UserPhone},
		},
		Actions:    []DashboardAction{{Label: "View account", URL: userURL}},
		OccurredAt: e.RegisteredAt, Environment: e.Environment,
	})
}

func BuildProfileSubmittedMessage(e ProfileSubmittedEvent) BlockKitMessage {
	reviewURL := DashboardRecordURL(e.AdminBaseURL, "profile-reviews", e.UserID)
	userURL := DashboardRecordURL(e.AdminBaseURL, "users", e.UserID)
	return BuildDashboardMessage(DashboardMessage{
		Title: "📝 Profile ready for review", Summary: "Review the submitted information and documents in Fundis Admin.",
		Fields: []MessageField{
			{Label: "Person", Value: DisplayName(e.UserName, e.UserEmail, "View account"), URL: userURL},
			{Label: "Profile", Value: "Pending review", URL: reviewURL},
			{Label: "Email", Value: e.UserEmail},
		},
		Actions:    []DashboardAction{{Label: "Review profile", URL: reviewURL}, {Label: "View account", URL: userURL}},
		OccurredAt: e.SubmittedAt, Environment: e.Environment,
	})
}
