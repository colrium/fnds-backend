package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"backend/integrations"
)

type SlackService struct {
	webhookURL string
}

func NewSlackService() *SlackService {
	return &SlackService{
		webhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
	}
}

func (s *SlackService) SendErrorNotification(errorCode int, errorMessage, environment, fileName string, lineNumber int) error {
	if s.webhookURL == "" {
		slog.Warn("Slack webhook URL not configured")
		return nil
	}

	msg := integrations.BuildDashboardMessage(integrations.DashboardMessage{
		Title: "🚨 Application error", Summary: errorMessage,
		Fields: []integrations.MessageField{
			{Label: "Error code", Value: fmt.Sprint(errorCode)},
			{Label: "Source", Value: fmt.Sprintf("%s:%d", fileName, lineNumber)},
		},
		Environment: environment, OccurredAt: time.Now().UTC(),
	})
	return integrations.NewSlackActivityClient(s.webhookURL).Send(context.Background(), msg)
}
