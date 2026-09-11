package integrations

import (
	"fmt"
	"strings"
	"time"
)

type AssessmentEvent struct {
	Title, Summary                               string
	UserID, UserName, JourneyID, SkillName       string
	AssessorID, AssessorName, ActorID, ActorName string
	Stage, Status                                string
	ScheduledFor, OccurredAt                     time.Time
	AdminBaseURL, Environment                    string
}

func AssessmentStageName(stage string) string {
	switch stage {
	case "profile_review":
		return "Profile review"
	case "written":
		return "Written assessment"
	case "oral":
		return "Oral assessment"
	case "practical":
		return "Practical assessment"
	default:
		return DisplayName(strings.ReplaceAll(stage, "_", " "), "", "Assessment")
	}
}

func BuildAssessmentMessage(e AssessmentEvent) BlockKitMessage {
	journeyURL := DashboardRecordURL(e.AdminBaseURL, "assessments/journeys", e.JourneyID)
	userURL := DashboardRecordURL(e.AdminBaseURL, "users", e.UserID)
	fields := []MessageField{
		{Label: "Artisan", Value: DisplayName(e.UserName, "", "View artisan"), URL: userURL},
		{Label: "Assessment", Value: DisplayName(e.SkillName, "", "Assessment journey"), URL: journeyURL},
		{Label: "Stage", Value: AssessmentStageName(e.Stage)},
		{Label: "Status", Value: e.Status},
	}
	if e.AssessorID != "" {
		fields = append(fields, MessageField{Label: "Assessor", Value: DisplayName(e.AssessorName, "", "View assessor"),
			URL: DashboardRecordURL(e.AdminBaseURL, "users", e.AssessorID)})
	}
	if e.ActorID != "" {
		fields = append(fields, MessageField{Label: "Reviewed by", Value: DisplayName(e.ActorName, "", "View administrator"),
			URL: DashboardRecordURL(e.AdminBaseURL, "users", e.ActorID)})
	}
	var details []string
	if !e.ScheduledFor.IsZero() {
		details = append(details, "*Scheduled for:* "+SlackDate(e.ScheduledFor))
	}
	return BuildDashboardMessage(DashboardMessage{
		Title: e.Title, Summary: e.Summary, Fields: fields, Details: details,
		Actions:    []DashboardAction{{Label: "View assessment", URL: journeyURL}, {Label: "View artisan", URL: userURL}},
		OccurredAt: e.OccurredAt, Environment: e.Environment,
	})
}

type PendingReview struct {
	UserID, UserName, JourneyID string
}

func BuildReviewSLAMessage(baseURL, environment, title, level string, reviews []PendingReview, now time.Time) BlockKitMessage {
	var details []string
	for _, review := range reviews[:min(len(reviews), 10)] {
		line := "• " + SlackRecordLink(baseURL, "profile-reviews", review.UserID, DisplayName(review.UserName, "", "View profile"))
		if review.JourneyID != "" {
			line += "  ·  " + SlackRecordLink(baseURL, "assessments/journeys", review.JourneyID, "View assessment")
		}
		details = append(details, line)
	}
	summary := fmt.Sprintf("%d profile review(s) pending beyond the %s threshold. Weekends are excluded.", len(reviews), level)
	if len(reviews) > 10 {
		summary += fmt.Sprintf(" Showing 10 of %d; open the review queue for the rest.", len(reviews))
	}
	return BuildDashboardMessage(DashboardMessage{
		Title: "⏰ " + title, Summary: summary, Details: details,
		Actions:    []DashboardAction{{Label: "Open review queue", URL: DashboardURL(baseURL, "profile-reviews")}},
		OccurredAt: now, Environment: environment,
	})
}

type NoShowAppointment struct {
	ID           string    `bson:"_id"`
	JourneyID    string    `bson:"journey_id"`
	UserID       string    `bson:"user_id"`
	AssessorID   string    `bson:"assessor_id"`
	Stage        string    `bson:"stage"`
	ScheduledFor time.Time `bson:"scheduled_for"`
	UserName     string    `bson:"-"`
	AssessorName string    `bson:"-"`
}

func BuildNoShowMessage(baseURL, environment string, appointments []NoShowAppointment, now time.Time) BlockKitMessage {
	var details []string
	for _, appointment := range appointments[:min(len(appointments), 10)] {
		line := "• " + SlackRecordLink(baseURL, "users", appointment.UserID, DisplayName(appointment.UserName, "", "View artisan"))
		line += "  ·  " + SlackRecordLink(baseURL, "assessments/journeys", appointment.JourneyID, AssessmentStageName(appointment.Stage))
		if appointment.AssessorID != "" {
			line += "\n   Assessor: " + SlackRecordLink(baseURL, "users", appointment.AssessorID, DisplayName(appointment.AssessorName, "", "View assessor"))
		}
		if !appointment.ScheduledFor.IsZero() {
			line += "\n   Scheduled: " + SlackDate(appointment.ScheduledFor)
		}
		details = append(details, line)
	}
	summary := fmt.Sprintf("%d appointment(s) are still marked scheduled more than two hours after their start time. Check the assessment before following up.", len(appointments))
	if len(appointments) > 10 {
		summary += fmt.Sprintf(" Showing 10 of %d; open assessments for the rest.", len(appointments))
	}
	return BuildDashboardMessage(DashboardMessage{
		Title: "⚠️ Possible assessment no-shows", Summary: summary, Details: details,
		Actions:    []DashboardAction{{Label: "Open assessments", URL: DashboardURL(baseURL, "assessments")}},
		OccurredAt: now, Environment: environment,
	})
}
