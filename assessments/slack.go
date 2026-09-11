package assessments

import (
	"context"
	"log/slog"
	"time"

	"backend/configs"
	"backend/integrations"
	"backend/notifications"
)

// notifyOps sends navigation to the existing admin record. Name enrichment is
// best effort and never changes the outcome of the assessment operation.
func notifyOps(ctx context.Context, event integrations.AssessmentEvent, skillID string) {
	if opsSlackClient == nil {
		return
	}
	names := notifications.AdminUserNames(ctx, event.UserID, event.AssessorID, event.ActorID)
	event.UserName = names[event.UserID]
	event.AssessorName = names[event.AssessorID]
	event.ActorName = names[event.ActorID]
	event.SkillName = notifications.AdminSkillName(ctx, skillID)
	event.AdminBaseURL = configs.AdminDashboardURL()
	event.Environment = configs.AppEnv()
	event.OccurredAt = time.Now().UTC()
	if err := opsSlackClient.Send(ctx, integrations.BuildAssessmentMessage(event)); err != nil {
		slog.Warn("failed to send ops notification", "title", event.Title, "error", err)
	}
}
