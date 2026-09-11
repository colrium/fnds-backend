package integrations

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func messageText(msg BlockKitMessage) string {
	text := msg.Text
	for _, block := range msg.Blocks {
		if block.Text != nil {
			text += "\n" + block.Text.Text
		}
		for _, field := range block.Fields {
			text += "\n" + field.Text
		}
		for _, element := range block.Elements {
			text += "\n" + element.Text
		}
	}
	return text
}

func TestNamedRecordAndActionDestinations(t *testing.T) {
	const base = "https://admin.fundis.co.ke"
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		msg  BlockKitMessage
		want []string
	}{
		{"vacancy on behalf", BuildVacancyCreatedMessage(VacancyCreatedEvent{
			VacancyID: "vac-1", VacancyTitle: "Plumber", CompanyName: "Acme",
			CreatorUserID: "admin-1", CreatorName: "Sam", OwnerUserID: "owner-1", OwnerName: "Amina",
			AdminBaseURL: base, CreatedAt: now,
		}), []string{"/vacancies/vac-1|Plumber>", "/vacancies/vac-1|Acme>", "/users/admin-1|Sam>", "/users/owner-1|Amina>", "/vacancies/vac-1|View vacancy →>"}},
		{"profile review", BuildProfileSubmittedMessage(ProfileSubmittedEvent{
			UserID: "user-1", UserName: "Jane", AdminBaseURL: base, SubmittedAt: now,
		}), []string{"/users/user-1|Jane>", "/profile-reviews/user-1|Pending review>", "/profile-reviews/user-1|Review profile →>"}},
		{"assessment decision", BuildAssessmentMessage(AssessmentEvent{
			Title: "Profile approved", UserID: "user-1", UserName: "Jane",
			JourneyID: "journey-1", SkillName: "Plumbing", ActorID: "admin-1", ActorName: "Sam",
			Stage: "profile_review", Status: "Approved", AdminBaseURL: base, OccurredAt: now,
		}), []string{"/users/user-1|Jane>", "/users/admin-1|Sam>", "/assessments/journeys/journey-1|Plumbing>", "/assessments/journeys/journey-1|View assessment →>", "Profile review"}},
		{"appointment", BuildAssessmentMessage(AssessmentEvent{
			Title: "Assessment booked", UserID: "user-1", UserName: "Jane", JourneyID: "journey-1",
			AssessorID: "assessor-1", AssessorName: "Peter", Stage: "oral", ScheduledFor: now,
			AdminBaseURL: base,
		}), []string{"/users/assessor-1|Peter>", "/assessments/journeys/journey-1|View assessment →>", "Oral assessment", SlackDate(now)}},
		{"review SLA", BuildReviewSLAMessage(base, "production", "Review overdue", "breach",
			[]PendingReview{{UserID: "user-1", UserName: "Jane", JourneyID: "journey-1"}}, now),
			[]string{"/profile-reviews/user-1|Jane>", "/assessments/journeys/journey-1|View assessment>", "/profile-reviews|Open review queue →>"}},
		{"no-show", BuildNoShowMessage(base, "production", []NoShowAppointment{{
			ID: "appointment-1", UserID: "user-1", UserName: "Jane", JourneyID: "journey-1",
			AssessorID: "assessor-1", AssessorName: "Peter", Stage: "oral", ScheduledFor: now,
		}}, now), []string{"/users/user-1|Jane>", "/users/assessor-1|Peter>", "/assessments/journeys/journey-1|Oral assessment>", "/assessments|Open assessments →>"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text := messageText(test.msg)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing named record or action %q in %s", want, text)
				}
			}
			// No callback buttons: incoming webhooks have no interactivity handler.
			for _, block := range test.msg.Blocks {
				if block.Type == "actions" {
					t.Error("navigation must work without Slack interaction callbacks")
				}
			}
			assertSlackLimits(t, test.msg)
		})
	}
}

func TestMissingNamesNeverExposeRecordIDs(t *testing.T) {
	const id = "77716f23-c9d1-4e31-807a-507197db93f5"
	messages := []BlockKitMessage{
		BuildVacancyCreatedMessage(VacancyCreatedEvent{VacancyID: id, CreatorUserID: id}),
		BuildApplicationMessage(ApplicationEvent{ApplicantID: id, VacancyID: id}),
		BuildRegistrationMessage(RegistrationEvent{UserID: id, UserEmail: "jane@example.com"}),
		BuildProfileSubmittedMessage(ProfileSubmittedEvent{UserID: id}),
		BuildAssessmentMessage(AssessmentEvent{UserID: id, JourneyID: id, AssessorID: id, ActorID: id}),
		BuildReviewSLAMessage("", "", "Overdue", "breach", []PendingReview{{UserID: id, JourneyID: id}}, time.Time{}),
		BuildNoShowMessage("", "", []NoShowAppointment{{ID: id, UserID: id, JourneyID: id, AssessorID: id}}, time.Time{}),
	}
	for i, msg := range messages {
		if strings.Contains(messageText(msg), id) {
			t.Errorf("message %d exposes a UUID instead of a readable fallback", i)
		}
		if strings.Contains(messageText(msg), "<http") {
			t.Errorf("message %d contains a broken link without a dashboard origin", i)
		}
	}
	if !strings.Contains(messageText(messages[2]), "jane@example.com") {
		t.Error("an email should be used when the name is missing")
	}
}

func TestSlackInputCannotInjectMentionsOrLinks(t *testing.T) {
	msg := BuildRegistrationMessage(RegistrationEvent{
		UserID: "user-1", UserName: "Jane|admin>\n<!channel><@U123>&",
		UserEmail: "<https://evil.example|Click me>", AdminBaseURL: "https://admin.fundis.co.ke",
	})
	text := messageText(msg)
	for _, forbidden := range []string{"<!channel>", "<@U123>", "<https://evil.example", "|admin>"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("unescaped Slack control sequence %q", forbidden)
		}
	}
	if !strings.Contains(text, "/users/user-1|Jane¦admin&gt;") {
		t.Error("escaped display name should still link to the user")
	}
}

func TestDashboardURLsRejectUnsafeOriginsAndEscapeRecordIDs(t *testing.T) {
	for _, base := range []string{"", "javascript:alert(1)", "//evil.example", "https://user:pass@example.com", "https://example.com?x=1", "https://example.com/#x"} {
		if got := DashboardRecordURL(base, "users", "u-1"); got != "" {
			t.Errorf("unsafe origin %q produced %q", base, got)
		}
	}
	id := "name/with?unsafe#characters"
	got := DashboardRecordURL("https://admin.fundis.co.ke/", "users", id)
	if got != "https://admin.fundis.co.ke/users/"+url.PathEscape(id) {
		t.Fatalf("ID escaped incorrectly: %q", got)
	}
}

func TestSlackLimitsAndCompleteLinksForLargeMessages(t *testing.T) {
	var fields []MessageField
	for range 25 {
		fields = append(fields, MessageField{Label: strings.Repeat("&", 100), Value: strings.Repeat("界<&", 1000)})
	}
	var details []string
	for range 10 {
		details = append(details, SlackRecordLink("https://admin.fundis.co.ke", "users", strings.Repeat("&", 500), strings.Repeat("<&", 100)))
	}
	msg := BuildDashboardMessage(DashboardMessage{
		Title: strings.Repeat("界", 200), Summary: strings.Repeat("<&", 1000),
		Fields: fields, Details: details, Environment: strings.Repeat("<&", 1000),
		Actions: []DashboardAction{{Label: "Open dashboard", URL: "https://admin.fundis.co.ke"}},
	})
	assertSlackLimits(t, msg)
	if !strings.Contains(msg.Text, "<https://admin.fundis.co.ke|Open dashboard →>") {
		t.Error("long push notification lost its dashboard action")
	}
	// User control characters are escaped; any remaining '<' must open a
	// complete generated link or Slack date token.
	control := regexp.MustCompile(`<[^<>]+>`)
	if strings.Contains(control.ReplaceAllString(messageText(msg), ""), "<") {
		t.Error("message contains a truncated link/control token")
	}
	var reviews []PendingReview
	for i := range 50 {
		reviews = append(reviews, PendingReview{UserID: fmt.Sprint(i), UserName: "Jane", JourneyID: fmt.Sprint(i)})
	}
	alert := BuildReviewSLAMessage("https://admin.fundis.co.ke", "production", "Overdue", "breach", reviews, time.Now())
	assertSlackLimits(t, alert)
	if !strings.Contains(messageText(alert), "Showing 10 of 50") || strings.Contains(messageText(alert), "/profile-reviews/10|") {
		t.Error("large alert must disclose its total and limit the visible record list")
	}
}

func assertSlackLimits(t *testing.T, msg BlockKitMessage) {
	t.Helper()
	if msg.Text == "" || utf8.RuneCountInString(msg.Text) > 4000 || len(msg.Blocks) > 50 {
		t.Errorf("invalid notification fallback or block count")
	}
	for _, block := range msg.Blocks {
		if block.Text != nil {
			limit := 3000
			if block.Type == "header" {
				limit = 150
			}
			if utf8.RuneCountInString(block.Text.Text) > limit {
				t.Errorf("%s exceeds Slack text limit", block.Type)
			}
		}
		if len(block.Fields) > 10 {
			t.Error("section exceeds 10 fields")
		}
		for _, field := range block.Fields {
			if utf8.RuneCountInString(field.Text) > 2000 {
				t.Error("field exceeds Slack limit")
			}
		}
	}
}
