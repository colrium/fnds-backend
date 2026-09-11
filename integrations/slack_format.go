package integrations

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// DashboardAction is navigation, not a Slack callback or an API mutation.
type DashboardAction struct {
	Label string
	URL   string
}

type DashboardMessage struct {
	Title       string
	Summary     string
	Fields      []MessageField
	Details     []string // mrkdwn composed with SlackText and SlackLink
	Actions     []DashboardAction
	OccurredAt  time.Time
	Environment string
}

func clipSlackText(value string, max int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max-1]) + "…"
}

// SlackText escapes Slack's control characters, including mention/link markup.
func SlackText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

// DisplayName never substitutes an internal identifier for a missing name.
func DisplayName(name, email, fallback string) string {
	for _, value := range []string{name, email, fallback} {
		if value = strings.Join(strings.Fields(value), " "); value != "" {
			return value
		}
	}
	return "User account"
}

// DashboardURL joins a trusted dashboard origin and a relative admin route.
// Invalid/missing configuration produces no link rather than a broken target.
func DashboardURL(baseURL, path string) string {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return ""
	}
	target := strings.TrimRight(base.String(), "/") + "/" + strings.TrimLeft(path, "/")
	if len(target) > 600 || strings.ContainsAny(target, "<>|\r\n") {
		return ""
	}
	return target
}

// DashboardRecordURL keeps record IDs inside their own URL path segment.
func DashboardRecordURL(baseURL, collection, id string) string {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return DashboardURL(baseURL, collection+"/"+url.PathEscape(id))
}

func slackNamedLink(target, label string) string {
	label = strings.ReplaceAll(strings.Join(strings.Fields(label), " "), "|", "¦")
	label = SlackText(clipSlackText(label, 60))
	if target == "" {
		return label
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || len(target) > 600 ||
		strings.ContainsAny(target, "<>|\r\n") {
		return label
	}
	link := "<" + SlackText(target) + "|" + label + ">"
	if utf8.RuneCountInString(link) > 1600 {
		return label
	}
	return link
}

func SlackLink(baseURL, path, displayText string) string {
	return slackNamedLink(DashboardURL(baseURL, path), displayText)
}

func SlackRecordLink(baseURL, collection, id, displayText string) string {
	return slackNamedLink(DashboardRecordURL(baseURL, collection, id), displayText)
}

func SlackDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return fmt.Sprintf("<!date^%d^{date_short_pretty} at {time}|%s>",
		value.Unix(), value.UTC().Format("02 Jan 2006, 15:04 UTC"))
}

// BuildDashboardMessage shares presentation across activity and operations
// notifications. Action links work with incoming webhooks without requiring
// Slack interactivity callbacks. Names remain clickable in every message.
func BuildDashboardMessage(in DashboardMessage) BlockKitMessage {
	in.Fields = in.Fields[:min(len(in.Fields), 20)]
	msg := buildActivityMessage(in.Title, in.Fields)
	fallback := []string{SlackText(clipSlackText(in.Title, 150))}
	if summary := clipSlackText(in.Summary, 500); summary != "" {
		section := Block{Type: "section", Text: &TextObject{Type: "mrkdwn", Text: SlackText(summary)}}
		msg.Blocks = append(msg.Blocks[:1], append([]Block{section}, msg.Blocks[1:]...)...)
		fallback = append(fallback, SlackText(summary))
	}
	for _, field := range in.Fields {
		if strings.TrimSpace(field.Value) != "" {
			value := SlackText(clipSlackText(field.Value, 250))
			if field.URL != "" {
				value = slackNamedLink(field.URL, field.Value)
			}
			fallback = append(fallback, SlackText(clipSlackText(field.Label, 60))+": "+value)
		}
	}
	// Do not cut a generated link in half when splitting alert lists.
	var paragraph string
	flush := func() {
		if paragraph != "" {
			msg.Blocks = append(msg.Blocks, Block{Type: "section", Text: &TextObject{Type: "mrkdwn", Text: paragraph}})
			paragraph = ""
		}
	}
	for i, detail := range in.Details {
		if i == 10 {
			break
		}
		if strings.TrimSpace(detail) == "" {
			continue
		}
		if utf8.RuneCountInString(detail) > 2800 {
			continue
		}
		if utf8.RuneCountInString(paragraph)+utf8.RuneCountInString(detail)+1 > 2800 {
			flush()
		}
		if paragraph != "" {
			paragraph += "\n"
		}
		paragraph += detail
		fallback = append(fallback, detail)
	}
	flush()
	var actions []string
	for _, action := range in.Actions {
		if action.URL == "" || strings.TrimSpace(action.Label) == "" {
			continue
		}
		link := slackNamedLink(action.URL, action.Label+" →")
		if !strings.HasPrefix(link, "<") {
			continue
		}
		if utf8.RuneCountInString(strings.Join(actions, "   ·   "))+utf8.RuneCountInString(link)+7 > 2800 {
			break
		}
		actions = append(actions, link)
		if len(actions) == 3 {
			break
		}
	}
	if len(actions) > 0 {
		msg.Blocks = append(msg.Blocks,
			Block{Type: "divider"},
			Block{Type: "section", Text: &TextObject{Type: "mrkdwn", Text: strings.Join(actions, "   ·   ")}},
		)
	}
	context := []string{"Fundis"}
	if in.Environment != "" {
		context = append(context, SlackText(clipSlackText(in.Environment, 40)))
	}
	if stamp := SlackDate(in.OccurredAt); stamp != "" {
		context = append(context, stamp)
	}
	msg.Blocks = append(msg.Blocks, Block{
		Type: "context", Elements: []TextObject{{Type: "mrkdwn", Text: strings.Join(context, "  ·  ")}},
	})
	// Top-level text supports mobile notifications and screen readers.
	actionText := strings.Join(actions, " · ")
	// Keep complete links/date tokens; reserve room for dashboard navigation
	// even when a long operations alert cannot fit in a push notification.
	budget := 3900 - utf8.RuneCountInString(actionText) - 80
	for _, line := range fallback {
		if utf8.RuneCountInString(msg.Text)+utf8.RuneCountInString(line)+1 > budget {
			msg.Text += "\nAdditional details are available in the dashboard."
			break
		}
		if msg.Text != "" {
			msg.Text += "\n"
		}
		msg.Text += line
	}
	if actionText != "" {
		msg.Text += "\n" + actionText
	}
	return msg
}
