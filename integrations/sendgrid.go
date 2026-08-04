package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const sendGridMailPath = "/v3/mail/send"

type sendGridAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sendGridPersonalization struct {
	To []sendGridAddress `json:"to"`
}

type sendGridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type sendGridRequest struct {
	Personalizations []sendGridPersonalization `json:"personalizations"`
	From             sendGridAddress           `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []sendGridContent         `json:"content"`
}

type SendGridClient struct {
	apiKey     string
	fromEmail  string
	fromName   string
	baseURL    string
	httpClient *http.Client
}

type SendGridOption func(*SendGridClient)

func WithSendGridBaseURL(baseURL string) SendGridOption {
	return func(client *SendGridClient) {
		client.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func WithSendGridHTTPClient(httpClient *http.Client) SendGridOption {
	return func(client *SendGridClient) {
		client.httpClient = httpClient
	}
}

func NewSendGridClient(apiKey, fromEmail, fromName string, options ...SendGridOption) *SendGridClient {
	client := &SendGridClient{
		apiKey:     apiKey,
		fromEmail:  fromEmail,
		fromName:   fromName,
		baseURL:    "https://api.sendgrid.com",
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (c *SendGridClient) Send(ctx context.Context, to, subject, htmlBody string) error {
	if strings.TrimSpace(c.apiKey) == "" || strings.TrimSpace(c.fromEmail) == "" {
		return fmt.Errorf("sendgrid: missing api key or sender email")
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("sendgrid: recipient email is required")
	}

	payload := sendGridRequest{
		Personalizations: []sendGridPersonalization{{
			To: []sendGridAddress{{Email: to}},
		}},
		From:    sendGridAddress{Email: c.fromEmail, Name: c.fromName},
		Subject: subject,
		Content: []sendGridContent{{Type: "text/html", Value: htmlBody}},
	}
	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(payload); err != nil {
		return fmt.Errorf("sendgrid: encoding request: %w", err)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+sendGridMailPath,
		body,
	)
	if err != nil {
		return fmt.Errorf("sendgrid: creating request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("sendgrid: sending request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf(
		"sendgrid: provider returned http %d: %s",
		response.StatusCode,
		strings.TrimSpace(string(responseBody)),
	)
}
