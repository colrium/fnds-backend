package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

type SlackService struct {
	webhookURL string
}

func NewSlackService() *SlackService {
	return &SlackService{
		webhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
	}
}

type slackMessage struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	Username string `json:"username"`
}

func (s *SlackService) SendErrorNotification(errorCode int, errorMessage, environment, fileName string, lineNumber int) error {
	if s.webhookURL == "" {
		slog.Warn("Slack webhook URL not configured")
		return nil
	}

	msg := slackMessage{
		Channel:  "#errors",
		Username: "Fundis App",
		Text:     fmt.Sprintf("Error Notification\n\nError message: %s\nError code: %d\nEnvironment: %s\nFile: %s\nLine: %d", errorMessage, errorCode, environment, fileName, lineNumber),
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal slack message: %w", err)
	}

	resp, err := http.Post(s.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to send slack notification: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}
