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
}

// buildActivityMessage renders a header block with the title, then a section
// block of field columns. Fields with a blank (trimmed) Value are skipped.
// If all fields are blank, no section block is emitted.
func buildActivityMessage(title string, fields []MessageField) BlockKitMessage {
	header := Block{
		Type: "header",
		Text: &TextObject{Type: "plain_text", Text: title},
	}

	columns := make([]TextObject, 0, len(fields))
	for _, f := range fields {
		if strings.TrimSpace(f.Value) == "" {
			continue
		}
		columns = append(columns, TextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*%s:*\n%s", f.Label, f.Value),
		})
	}

	blocks := []Block{header}
	if len(columns) > 0 {
		blocks = append(blocks, Block{Type: "section", Fields: columns})
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
	CreatedAt     time.Time
	AdminBaseURL  string
}

// ApplicationEvent carries application fields for building the notification.
type ApplicationEvent struct {
	ApplicantName  string
	ApplicantEmail string
	ApplicantPhone string
	ApplicantID    string
	VacancyID      string
	VacancyTitle   string
	AppliedAt      time.Time
	AdminBaseURL   string
}

// RegistrationEvent carries registration fields for building the notification.
type RegistrationEvent struct {
	UserName     string
	UserEmail    string
	UserPhone    string
	UserID       string
	UserType     string
	RegisteredAt time.Time
	AdminBaseURL string
}

// SlackLink returns a Slack mrkdwn link (<baseURL/path|displayText>) when
// baseURL is non-empty, or just displayText when baseURL is empty.
func SlackLink(baseURL, path, displayText string) string {
	if baseURL == "" {
		return displayText
	}
	return fmt.Sprintf("<%s/%s|%s>", strings.TrimRight(baseURL, "/"), strings.TrimLeft(path, "/"), displayText)
}

// BuildVacancyCreatedMessage translates a VacancyCreatedEvent into a Block Kit message.
func BuildVacancyCreatedMessage(e VacancyCreatedEvent) BlockKitMessage {
	vacancyDisplay := SlackLink(e.AdminBaseURL, "vacancies/"+e.VacancyID, e.VacancyID)
	creatorDisplay := SlackLink(e.AdminBaseURL, "users/"+e.CreatorUserID, e.CreatorUserID)
	return buildActivityMessage("New Vacancy Created", []MessageField{
		{Label: "Vacancy ID", Value: vacancyDisplay},
		{Label: "Title", Value: e.VacancyTitle},
		{Label: "Created By", Value: creatorDisplay},
		{Label: "Created At", Value: e.CreatedAt.Format(time.RFC3339)},
	})
}

// BuildApplicationMessage translates an ApplicationEvent into a Block Kit message.
func BuildApplicationMessage(e ApplicationEvent) BlockKitMessage {
	applicantDisplay := SlackLink(e.AdminBaseURL, "users/"+e.ApplicantID, e.ApplicantID)
	vacancyDisplay := SlackLink(e.AdminBaseURL, "vacancies/"+e.VacancyID, e.VacancyID)
	return buildActivityMessage("New Vacancy Application", []MessageField{
		{Label: "Applicant", Value: e.ApplicantName},
		{Label: "Email", Value: e.ApplicantEmail},
		{Label: "Phone", Value: e.ApplicantPhone},
		{Label: "Applicant ID", Value: applicantDisplay},
		{Label: "Vacancy ID", Value: vacancyDisplay},
		{Label: "Vacancy Title", Value: e.VacancyTitle},
		{Label: "Applied At", Value: e.AppliedAt.Format(time.RFC3339)},
	})
}

// BuildRegistrationMessage translates a RegistrationEvent into a Block Kit message.
func BuildRegistrationMessage(e RegistrationEvent) BlockKitMessage {
	userDisplay := SlackLink(e.AdminBaseURL, "users/"+e.UserID, e.UserID)
	return buildActivityMessage("New User Registration", []MessageField{
		{Label: "Name", Value: e.UserName},
		{Label: "User Type", Value: e.UserType},
		{Label: "Email", Value: e.UserEmail},
		{Label: "Phone", Value: e.UserPhone},
		{Label: "User ID", Value: userDisplay},
		{Label: "Registered At", Value: e.RegisteredAt.Format(time.RFC3339)},
	})
}

// ProfileSubmittedEvent carries fields for the profile-submitted-for-review notification.
type ProfileSubmittedEvent struct {
	UserID       string
	UserName     string
	UserEmail    string
	SubmittedAt  time.Time
	AdminBaseURL string
}

// BuildProfileSubmittedMessage translates a ProfileSubmittedEvent into a Block Kit message.
func BuildProfileSubmittedMessage(e ProfileSubmittedEvent) BlockKitMessage {
	reviewLink := SlackLink(e.AdminBaseURL, "profile-reviews/"+e.UserID, e.UserID)
	return buildActivityMessage("Profile Submitted for Review", []MessageField{
		{Label: "Name", Value: e.UserName},
		{Label: "Email", Value: e.UserEmail},
		{Label: "Profile Review", Value: reviewLink},
		{Label: "Submitted At", Value: e.SubmittedAt.Format(time.RFC3339)},
	})
}
