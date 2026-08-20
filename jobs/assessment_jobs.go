package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"backend/configs"
	"backend/integrations"
	"backend/notifications"
	"backend/timeutil"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// reviewSLAWarningHours is when a warning alert fires (36h after submission).
const reviewSLAWarningHours = 36

// reviewSLABreachHours is when a breach alert fires (48h after submission).
const reviewSLABreachHours = 48

// CheckReviewSLA finds journeys stuck in profile_review and sends Slack alerts
// at the 36h (warning) and 48h (breach) marks, excluding weekends from the
// elapsed time calculation. Called daily at 09:00 EAT by the scheduler.
func CheckReviewSLA() {
	ctx := context.Background()
	now := time.Now().UTC()

	// Look back far enough to cover cases where a weekend extended the window.
	// 5 calendar days is enough to cover 48 business hours even with a full
	// weekend in between (48h + 48h weekend = 96h = 4 days).
	lookbackCutoff := now.Add(-5 * 24 * time.Hour)

	filter := bson.M{
		"current_stage":                "profile_review",
		"stages.profile_review.status": bson.M{"$in": bson.A{"available", "in_progress", "awaiting_review"}},
		"updated_at":                   bson.M{"$lte": now, "$gte": lookbackCutoff},
	}

	cursor, err := configs.MI.DB.Collection("assessment_journeys").Find(
		ctx,
		filter,
		options.Find().SetLimit(100),
	)
	if err != nil {
		slog.Error("review SLA check failed", "error", err)
		return
	}
	defer func() { _ = cursor.Close(ctx) }()

	type journey struct {
		ID        string    `bson:"_id"`
		UserID    string    `bson:"user_id"`
		UpdatedAt time.Time `bson:"updated_at"`
	}

	var journeys []journey
	if err := cursor.All(ctx, &journeys); err != nil {
		slog.Error("review SLA check decode failed", "error", err)
		return
	}

	var warningJourneys, breachJourneys int
	var warningUserIDs, breachUserIDs []string
	for _, j := range journeys {
		elapsed := timeutil.BusinessHoursSince(j.UpdatedAt, now)
		if elapsed >= float64(reviewSLABreachHours) {
			breachJourneys++
			breachUserIDs = append(breachUserIDs, j.UserID)
		} else if elapsed >= float64(reviewSLAWarningHours) {
			warningJourneys++
			warningUserIDs = append(warningUserIDs, j.UserID)
		}
	}

	if warningJourneys > 0 {
		sendSLASlackAlert(ctx, warningJourneys, "warning", fmt.Sprintf(
			"Profile reviews approaching %dh SLA breach", reviewSLABreachHours,
		), warningUserIDs)
	}
	if breachJourneys > 0 {
		sendSLASlackAlert(ctx, breachJourneys, "breach", fmt.Sprintf(
			"Profile reviews have BREACHED %dh SLA", reviewSLABreachHours,
		), breachUserIDs)
	}
}

func sendSLASlackAlert(ctx context.Context, count int, level, title string, userIDs []string) {
	adminBaseURL := configs.AdminDashboardURL()

	// Build a linked list of profile reviews (limit to 10 to avoid oversized messages).
	const maxLinks = 10
	var linkLines string
	for i, uid := range userIDs {
		if i >= maxLinks {
			linkLines += fmt.Sprintf("\n... and %d more", len(userIDs)-maxLinks)
			break
		}
		link := integrations.SlackLink(adminBaseURL, "profile-reviews/"+uid, uid)
		linkLines += fmt.Sprintf("\n• %s", link)
	}

	blocks := []integrations.Block{
		{
			Type: "header",
			Text: &integrations.TextObject{Type: "plain_text", Text: title},
		},
		{
			Type: "section",
			Text: &integrations.TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*%d* profile review(s) pending beyond %s threshold (weekends excluded).",
					count, level),
			},
		},
	}

	if linkLines != "" {
		blocks = append(blocks, integrations.Block{
			Type: "section",
			Text: &integrations.TextObject{
				Type: "mrkdwn",
				Text: "*Affected profiles:*" + linkLines,
			},
		})
	}

	msg := integrations.BlockKitMessage{Blocks: blocks}
	webhookURL := configs.SlackOpsNotifsURL()
	if webhookURL == "" {
		webhookURL = configs.SlackWebhookURL()
	}
	if webhookURL == "" {
		slog.Debug("review SLA slack alert skipped; webhook not configured", "level", level)
		return
	}
	client := integrations.NewSlackActivityClient(webhookURL)
	if err := client.Send(ctx, msg); err != nil {
		slog.Error("review SLA slack alert failed", "level", level, "error", err)
	} else {
		slog.Info("review SLA alert sent", "level", level, "count", count)
	}
}

// CheckAppointmentReminders finds upcoming appointments and sends reminders at
// 24h and 2h before the scheduled time. Uses a reminder_sent_at field to
// prevent duplicate notifications on re-runs.
func CheckAppointmentReminders() {
	ctx := context.Background()
	now := time.Now().UTC()

	// 24h window: appointments between 23.5h and 24.5h from now.
	sendReminders(ctx, now, now.Add(23*time.Hour+30*time.Minute), now.Add(24*time.Hour+30*time.Minute), "24h")

	// 2h window: appointments between 1.5h and 2.5h from now.
	sendReminders(ctx, now, now.Add(1*time.Hour+30*time.Minute), now.Add(2*time.Hour+30*time.Minute), "2h")
}

// CheckNoShows detects appointments that were scheduled in the past but
// remain in "scheduled" status (no report filed, no status change). These
// are likely no-shows and ops should be alerted via Slack.
func CheckNoShows() {
	ctx := context.Background()
	now := time.Now().UTC()

	// Look for appointments scheduled more than 2 hours ago still in "scheduled" status.
	cutoff := now.Add(-2 * time.Hour)

	filter := bson.M{
		"status":             "scheduled",
		"scheduled_for":      bson.M{"$lte": cutoff},
		"no_show_alerted_at": bson.M{"$exists": false},
	}

	cursor, err := configs.MI.DB.Collection("assessment_appointments").Find(
		ctx,
		filter,
		options.Find().SetLimit(50),
	)
	if err != nil {
		slog.Error("no-show check query failed", "error", err)
		return
	}
	defer func() { _ = cursor.Close(ctx) }()

	type appointment struct {
		ID           string    `bson:"_id"`
		UserID       string    `bson:"user_id"`
		AssessorID   string    `bson:"assessor_id"`
		Stage        string    `bson:"stage"`
		ScheduledFor time.Time `bson:"scheduled_for"`
	}

	var appointments []appointment
	if err := cursor.All(ctx, &appointments); err != nil {
		slog.Error("no-show check decode failed", "error", err)
		return
	}

	if len(appointments) == 0 {
		return
	}

	// Send Slack alert for ops
	msg := integrations.BlockKitMessage{
		Blocks: []integrations.Block{
			{
				Type: "header",
				Text: &integrations.TextObject{Type: "plain_text", Text: "Assessment No-Shows Detected"},
			},
			{
				Type: "section",
				Text: &integrations.TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*%d* appointment(s) appear to be no-shows (scheduled >2h ago, still in 'scheduled' status). Admin may need to reassign or follow up.",
						len(appointments)),
				},
			},
		},
	}

	webhookURL := configs.SlackOpsNotifsURL()
	if webhookURL == "" {
		webhookURL = configs.SlackWebhookURL()
	}
	client := integrations.NewSlackActivityClient(webhookURL)
	if err := client.Send(ctx, msg); err != nil {
		slog.Error("no-show slack alert failed", "error", err)
	} else {
		slog.Info("no-show alert sent", "count", len(appointments))
	}

	// Mark as alerted so we don't re-notify
	for _, appt := range appointments {
		_, updateErr := configs.MI.DB.Collection("assessment_appointments").UpdateOne(
			ctx,
			bson.M{"_id": appt.ID},
			bson.M{"$set": bson.M{"no_show_alerted_at": now}},
		)
		if updateErr != nil {
			slog.Warn("failed to mark no-show alert",
				"appointment_id", appt.ID, "error", updateErr)
		}
	}
}

func sendReminders(ctx context.Context, now, windowStart, windowEnd time.Time, label string) {
	filter := bson.M{
		"scheduled_for": bson.M{
			"$gte": windowStart,
			"$lte": windowEnd,
		},
		"status": bson.M{"$in": bson.A{"scheduled", "awaiting_payment"}},
		// Only send if reminder not already sent for this window.
		fmt.Sprintf("reminder_%s_sent_at", label): bson.M{"$exists": false},
	}

	cursor, err := configs.MI.DB.Collection("assessment_appointments").Find(
		ctx,
		filter,
		options.Find().SetLimit(100),
	)
	if err != nil {
		slog.Error("appointment reminder query failed", "window", label, "error", err)
		return
	}
	defer func() { _ = cursor.Close(ctx) }()

	type appointment struct {
		ID           string    `bson:"_id"`
		UserID       string    `bson:"user_id"`
		Stage        string    `bson:"stage"`
		ScheduledFor time.Time `bson:"scheduled_for"`
		MeetingLink  string    `bson:"meeting_link,omitempty"`
	}

	var appointments []appointment
	if err := cursor.All(ctx, &appointments); err != nil {
		slog.Error("appointment reminder decode failed", "window", label, "error", err)
		return
	}

	if len(appointments) == 0 {
		return
	}

	store := notifications.NewMongoStore()
	for _, appt := range appointments {
		message := fmt.Sprintf(
			"Your %s assessment is scheduled for %s.",
			appt.Stage,
			appt.ScheduledFor.Format("Mon, 02 Jan at 15:04"),
		)
		if appt.Stage == "oral" && appt.MeetingLink != "" {
			message += fmt.Sprintf(" Join here: %s", appt.MeetingLink)
		}

		// Create in-app notification.
		notif := notifications.Notification{
			UserID:    appt.UserID,
			Type:      notifications.TypeAppointmentReminder,
			Title:     "Appointment Reminder",
			Message:   message,
			ActionURL: "/assessments/journey",
		}
		if err := store.Create(ctx, notif); err != nil {
			slog.Error("appointment reminder notification failed",
				"appointment_id", appt.ID, "error", err)
			continue
		}

		// Also create an SMS delivery notification.
		smsNotif := notifications.Notification{
			ID:        uuid.New().String(),
			UserID:    appt.UserID,
			Type:      notifications.TypeAppointmentReminder,
			Title:     "Appointment Reminder",
			Message:   message,
			ActionURL: "/assessments/journey",
			Delivery: &notifications.Delivery{
				Channel: notifications.DeliveryChannelSMS,
				To:      "", // Phone resolved at delivery time by dispatcher.
				Body:    message,
			},
		}
		if err := store.Create(ctx, smsNotif); err != nil {
			slog.Error("appointment reminder SMS notification failed",
				"appointment_id", appt.ID, "error", err)
		}

		// Mark reminder as sent so re-runs do not re-notify.
		_, updateErr := configs.MI.DB.Collection("assessment_appointments").UpdateOne(
			ctx,
			bson.M{"_id": appt.ID},
			bson.M{"$set": bson.M{
				fmt.Sprintf("reminder_%s_sent_at", label): now,
			}},
		)
		if updateErr != nil {
			slog.Error("appointment reminder mark failed",
				"appointment_id", appt.ID, "error", updateErr)
		}
	}

	slog.Info("appointment reminders sent", "window", label, "count", len(appointments))
}
