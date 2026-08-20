package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"
)

// randomString generates a random string of the given length using the provided rng.
func randomString(rng *rand.Rand, maxLen int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 _-@.+"
	length := rng.Intn(maxLen + 1)
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rng.Intn(len(charset))]
	}
	return string(b)
}

// randomNonEmptyString generates a non-empty, non-whitespace string.
func randomNonEmptyString(rng *rand.Rand, maxLen int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-@.+"
	length := rng.Intn(maxLen) + 1
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rng.Intn(len(charset))]
	}
	return string(b)
}

// randomWhitespaceOrEmpty generates a string that is empty or contains only whitespace.
func randomWhitespaceOrEmpty(rng *rand.Rand) string {
	length := rng.Intn(5) // 0 to 4 whitespace chars
	return strings.Repeat(" ", length)
}

// **Validates: Requirements 1.2, 2.2, 3.2, 4.3**
//
// Property 1: Required fields are included when available
//
// For any set of MessageField values where Value is non-empty, those field
// values appear in the rendered block columns.
func TestProperty1_RequiredFieldsIncludedWhenAvailable(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	const iterations = 100

	for i := 0; i < iterations; i++ {
		// Generate a random title and a random number of fields with non-empty values.
		title := randomNonEmptyString(rng, 30)
		numFields := rng.Intn(8) + 1 // 1 to 8 fields
		fields := make([]MessageField, numFields)
		for j := range fields {
			fields[j] = MessageField{
				Label: randomNonEmptyString(rng, 15),
				Value: randomNonEmptyString(rng, 40),
			}
		}

		msg := buildActivityMessage(title, fields)

		// The message must have at least 2 blocks: header + section.
		if len(msg.Blocks) < 2 {
			t.Fatalf("iteration %d: expected at least 2 blocks (header+section), got %d", i, len(msg.Blocks))
		}

		section := msg.Blocks[1]
		if section.Type != "section" {
			t.Fatalf("iteration %d: expected second block to be section, got %q", i, section.Type)
		}

		// Every non-empty field value must appear in the section's Fields.
		if len(section.Fields) != numFields {
			t.Fatalf("iteration %d: expected %d fields in section, got %d", i, numFields, len(section.Fields))
		}

		for j, f := range fields {
			found := false
			for _, col := range section.Fields {
				if strings.Contains(col.Text, f.Value) && strings.Contains(col.Text, f.Label) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("iteration %d: field[%d] label=%q value=%q not found in section fields", i, j, f.Label, f.Value)
			}
		}
	}
}

// **Validates: Requirements 4.4**
//
// Property 2: Empty fields are omitted
//
// For any set of MessageField values where Value is empty/whitespace, those
// fields produce NO entry in the section Fields slice.
func TestProperty2_EmptyFieldsAreOmitted(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	const iterations = 100

	for i := 0; i < iterations; i++ {
		title := randomNonEmptyString(rng, 30)

		// Mix of non-empty and empty/whitespace fields.
		numFields := rng.Intn(8) + 2
		fields := make([]MessageField, numFields)
		expectedCount := 0

		for j := range fields {
			fields[j].Label = randomNonEmptyString(rng, 15)
			if rng.Float32() < 0.5 {
				// Non-empty value — should appear.
				fields[j].Value = randomNonEmptyString(rng, 40)
				expectedCount++
			} else {
				// Empty or whitespace-only value — should be omitted.
				fields[j].Value = randomWhitespaceOrEmpty(rng)
			}
		}

		msg := buildActivityMessage(title, fields)

		// Count actual fields in the section (if present).
		actualCount := 0
		if len(msg.Blocks) > 1 {
			section := msg.Blocks[1]
			if section.Type == "section" {
				actualCount = len(section.Fields)
			}
		}

		if actualCount != expectedCount {
			t.Fatalf("iteration %d: expected %d non-empty fields in section, got %d", i, expectedCount, actualCount)
		}

		// Verify that no empty/whitespace value produced a column.
		// A column for a field would contain "*Label:*\n" followed by the value.
		// Since empty fields have no meaningful value, we check that no column
		// was formatted from a field whose value was blank.
		if len(msg.Blocks) > 1 && msg.Blocks[1].Type == "section" {
			for _, f := range fields {
				if strings.TrimSpace(f.Value) != "" {
					continue
				}
				// This field had an empty value; it must NOT have a matching column.
				marker := "*" + f.Label + ":*\n"
				for _, col := range msg.Blocks[1].Fields {
					// Only flag if the column text starts with the exact label marker
					// AND the remainder after the marker is empty/whitespace (which would
					// indicate the empty value was rendered). In practice, since the
					// builder skips empty values entirely, no column with this marker
					// should exist unless another field shares the same label with a
					// non-empty value. We only fail if the entire column matches the
					// pattern of an empty-value field.
					if strings.Contains(col.Text, marker) {
						// Check if this column could belong to a different field with
						// the same label but a non-empty value.
						suffix := strings.TrimPrefix(col.Text, marker)
						if suffix == "" || strings.TrimSpace(suffix) == "" {
							t.Fatalf("iteration %d: empty field label=%q produced a column", i, f.Label)
						}
					}
				}
			}
		}
	}
}

// **Validates: Requirements 4.2**
//
// Property 3: Header identifies the activity type
//
// For any activity event, the built Block Kit message contains exactly one
// header block whose text identifies the event's activity type (the title).
func TestProperty3_HeaderIdentifiesActivityType(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const iterations = 100

	for i := 0; i < iterations; i++ {
		title := randomNonEmptyString(rng, 50)
		numFields := rng.Intn(6)
		fields := make([]MessageField, numFields)
		for j := range fields {
			fields[j] = MessageField{
				Label: randomNonEmptyString(rng, 15),
				Value: randomString(rng, 30),
			}
		}

		msg := buildActivityMessage(title, fields)

		// First block must be a header.
		if len(msg.Blocks) == 0 {
			t.Fatalf("iteration %d: message has no blocks", i)
		}

		header := msg.Blocks[0]
		if header.Type != "header" {
			t.Fatalf("iteration %d: first block type = %q, want \"header\"", i, header.Type)
		}
		if header.Text == nil {
			t.Fatalf("iteration %d: header block has nil Text", i)
		}
		if header.Text.Text != title {
			t.Fatalf("iteration %d: header text = %q, want %q", i, header.Text.Text, title)
		}
		if header.Text.Type != "plain_text" {
			t.Fatalf("iteration %d: header text type = %q, want \"plain_text\"", i, header.Text.Type)
		}

		// Exactly one header block in the entire message.
		headerCount := 0
		for _, b := range msg.Blocks {
			if b.Type == "header" {
				headerCount++
			}
		}
		if headerCount != 1 {
			t.Fatalf("iteration %d: expected exactly 1 header block, got %d", i, headerCount)
		}
	}
}

// **Validates: Requirements 4.1**
//
// Property 4: Messages are valid Block Kit that round-trips
//
// For any activity event, the built message serializes to JSON containing a
// "blocks" array, and decoding that JSON back produces an identical structure.
func TestProperty4_MessagesRoundTripThroughJSON(t *testing.T) {
	rng := rand.New(rand.NewSource(123))
	const iterations = 100

	for i := 0; i < iterations; i++ {
		title := randomNonEmptyString(rng, 40)
		numFields := rng.Intn(10)
		fields := make([]MessageField, numFields)
		for j := range fields {
			fields[j] = MessageField{
				Label: randomNonEmptyString(rng, 20),
				Value: randomString(rng, 50),
			}
		}

		msg := buildActivityMessage(title, fields)

		// Serialize to JSON.
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("iteration %d: json.Marshal failed: %v", i, err)
		}

		// Verify the JSON has a "blocks" key.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("iteration %d: JSON is not a valid object: %v", i, err)
		}
		if _, ok := raw["blocks"]; !ok {
			t.Fatalf("iteration %d: JSON missing \"blocks\" key", i)
		}

		// Verify the "blocks" value is an array.
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw["blocks"], &blocks); err != nil {
			t.Fatalf("iteration %d: \"blocks\" is not a valid array: %v", i, err)
		}

		// Deserialize back and compare.
		var decoded BlockKitMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("iteration %d: json.Unmarshal failed: %v", i, err)
		}

		// Re-serialize and compare bytes for structural equality.
		reEncoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("iteration %d: re-marshal failed: %v", i, err)
		}

		if string(data) != string(reEncoded) {
			t.Fatalf("iteration %d: round-trip mismatch\noriginal:  %s\nre-encoded: %s", i, string(data), string(reEncoded))
		}
	}
}

// ---------------------------------------------------------------------------
// Unit + Property tests for SlackActivityClient.Send
// ---------------------------------------------------------------------------

// **Validates: Requirements 5.3, 6.2, 6.3, 6.4**
//
// TestSlackActivityClientSend_Success verifies that Send issues a single POST
// to the configured webhook URL with Content-Type: application/json, a `blocks`
// payload, and no `channel` override key.
func TestSlackActivityClientSend_Success(t *testing.T) {
	var gotMethod string
	var gotContentType string
	var gotURL string
	var gotBody []byte

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotMethod = req.Method
		gotContentType = req.Header.Get("Content-Type")
		gotURL = req.URL.String()
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		gotBody = body
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	webhookURL := "https://hooks.slack.com/services/T00/B00/xxx"
	client := NewSlackActivityClient(webhookURL, WithSlackActivityHTTPClient(httpClient))

	msg := BlockKitMessage{
		Blocks: []Block{
			{Type: "header", Text: &TextObject{Type: "plain_text", Text: "Test Event"}},
			{Type: "section", Fields: []TextObject{{Type: "mrkdwn", Text: "*ID:*\n123"}}},
		},
	}

	ctx := context.Background()
	err := client.Send(ctx, msg)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	// Assert single POST to the configured webhook URL.
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotURL != webhookURL {
		t.Fatalf("url = %q, want %q", gotURL, webhookURL)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}

	// Assert the body has a "blocks" key and no "channel" key.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := payload["blocks"]; !ok {
		t.Fatal("payload missing \"blocks\" key")
	}
	if _, ok := payload["channel"]; ok {
		t.Fatal("payload contains a \"channel\" key — the activity client must not override the webhook channel")
	}
}

// **Validates: Requirements 5.4, 6.3**
//
// TestSlackActivityClientSend_EmptyWebhook verifies that when the webhook URL
// is empty, ErrSlackWebhookNotConfigured is returned and no HTTP request is made.
func TestSlackActivityClientSend_EmptyWebhook(t *testing.T) {
	requestMade := false
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestMade = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	client := NewSlackActivityClient("", WithSlackActivityHTTPClient(httpClient))
	msg := BlockKitMessage{
		Blocks: []Block{{Type: "header", Text: &TextObject{Type: "plain_text", Text: "Test"}}},
	}

	err := client.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for empty webhook, got nil")
	}
	if !errors.Is(err, ErrSlackWebhookNotConfigured) {
		t.Fatalf("error = %v, want ErrSlackWebhookNotConfigured", err)
	}
	if requestMade {
		t.Fatal("HTTP request was made despite empty webhook URL")
	}
}

// **Validates: Requirements 5.2, 6.4**
//
// TestSlackActivityClientSend_SingleAttemptOnFailure verifies that on a server
// error (500), exactly one HTTP request is made (no retries).
func TestSlackActivityClientSend_SingleAttemptOnFailure(t *testing.T) {
	callCount := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		callCount++
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("internal error")),
			Header:     make(http.Header),
		}, nil
	})}

	client := NewSlackActivityClient(
		"https://hooks.slack.com/services/T00/B00/xxx",
		WithSlackActivityHTTPClient(httpClient),
	)
	msg := BlockKitMessage{
		Blocks: []Block{{Type: "header", Text: &TextObject{Type: "plain_text", Text: "Test"}}},
	}

	err := client.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	if callCount != 1 {
		t.Fatalf("httpClient.Do called %d times, want exactly 1 (no retries)", callCount)
	}
}

// **Validates: Requirements 5.3**
//
// Property 5: Non-2xx responses are delivery failures
//
// For any HTTP status code returned by the webhook, Send returns an error if
// and only if the status is outside the 200-299 range.
func TestProperty5_NonTwoXXResponsesAreDeliveryFailures(t *testing.T) {
	rng := rand.New(rand.NewSource(55))
	const iterations = 150

	for i := 0; i < iterations; i++ {
		// Generate a random status code between 100 and 599.
		statusCode := 100 + rng.Intn(500) // 100..599

		httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: statusCode,
				Body:       io.NopCloser(strings.NewReader("response body")),
				Header:     make(http.Header),
			}, nil
		})}

		client := NewSlackActivityClient(
			"https://hooks.slack.com/services/T00/B00/test",
			WithSlackActivityHTTPClient(httpClient),
		)
		msg := BlockKitMessage{
			Blocks: []Block{{Type: "header", Text: &TextObject{Type: "plain_text", Text: "Test"}}},
		}

		err := client.Send(context.Background(), msg)
		isSuccess := statusCode >= 200 && statusCode <= 299

		if isSuccess && err != nil {
			t.Fatalf("iteration %d: status %d is 2xx but Send returned error: %v", i, statusCode, err)
		}
		if !isSuccess && err == nil {
			t.Fatalf("iteration %d: status %d is non-2xx but Send returned nil error", i, statusCode)
		}
	}
}

// **Validates: Requirements 5.4, 6.2**
//
// Property 6: Unconfigured webhook skips delivery
//
// When webhookURL is empty (or whitespace-only), no HTTP request is issued and
// the sentinel error ErrSlackWebhookNotConfigured is returned.
func TestProperty6_UnconfiguredWebhookSkipsDelivery(t *testing.T) {
	emptyWebhookVariants := []string{
		"",
		" ",
		"  ",
		"\t",
		"\n",
		" \t\n ",
	}

	for _, webhook := range emptyWebhookVariants {
		requestMade := false
		httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestMade = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		})}

		client := NewSlackActivityClient(webhook, WithSlackActivityHTTPClient(httpClient))
		msg := BlockKitMessage{
			Blocks: []Block{{Type: "header", Text: &TextObject{Type: "plain_text", Text: "Test"}}},
		}

		err := client.Send(context.Background(), msg)
		if !errors.Is(err, ErrSlackWebhookNotConfigured) {
			t.Fatalf("webhook=%q: expected ErrSlackWebhookNotConfigured, got %v", webhook, err)
		}
		if requestMade {
			t.Fatalf("webhook=%q: HTTP request was made despite unconfigured webhook", webhook)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests for slackLink helper function
// ---------------------------------------------------------------------------

func TestSlackLink_WithBaseURL(t *testing.T) {
	got := SlackLink("https://admin.fundis.co.ke", "users/abc123", "abc123")
	want := "<https://admin.fundis.co.ke/users/abc123|abc123>"
	if got != want {
		t.Fatalf("SlackLink = %q, want %q", got, want)
	}
}

func TestSlackLink_WithTrailingSlash(t *testing.T) {
	got := SlackLink("https://admin.fundis.co.ke/", "users/abc123", "abc123")
	want := "<https://admin.fundis.co.ke/users/abc123|abc123>"
	if got != want {
		t.Fatalf("SlackLink = %q, want %q", got, want)
	}
}

func TestSlackLink_EmptyBaseURL(t *testing.T) {
	got := SlackLink("", "users/abc123", "abc123")
	want := "abc123"
	if got != want {
		t.Fatalf("SlackLink = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Tests for Build*Message with AdminBaseURL set (links rendered)
// ---------------------------------------------------------------------------

func TestBuildVacancyCreatedMessage_WithAdminLinks(t *testing.T) {
	e := VacancyCreatedEvent{
		VacancyID:     "v-123",
		VacancyTitle:  "Plumber",
		CreatorUserID: "u-1",
		AdminBaseURL:  "https://admin.fundis.co.ke",
	}
	msg := BuildVacancyCreatedMessage(e)

	// Verify the section block contains the mrkdwn links.
	if len(msg.Blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(msg.Blocks))
	}
	section := msg.Blocks[1]

	assertFieldContains(t, section.Fields, "Vacancy ID", "<https://admin.fundis.co.ke/vacancies/v-123|v-123>")
	assertFieldContains(t, section.Fields, "Created By", "<https://admin.fundis.co.ke/users/u-1|u-1>")
}

func TestBuildVacancyCreatedMessage_WithoutAdminLinks(t *testing.T) {
	e := VacancyCreatedEvent{
		VacancyID:     "v-123",
		VacancyTitle:  "Plumber",
		CreatorUserID: "u-1",
		AdminBaseURL:  "",
	}
	msg := BuildVacancyCreatedMessage(e)

	if len(msg.Blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(msg.Blocks))
	}
	section := msg.Blocks[1]

	// Should have plain text, not links.
	assertFieldContains(t, section.Fields, "Vacancy ID", "v-123")
	assertFieldNotContains(t, section.Fields, "Vacancy ID", "<")
	assertFieldContains(t, section.Fields, "Created By", "u-1")
	assertFieldNotContains(t, section.Fields, "Created By", "<")
}

func TestBuildApplicationMessage_WithAdminLinks(t *testing.T) {
	e := ApplicationEvent{
		ApplicantName:  "Jane Doe",
		ApplicantEmail: "jane@example.com",
		ApplicantPhone: "+254700000000",
		ApplicantID:    "u-42",
		VacancyID:      "v-99",
		VacancyTitle:   "Electrician",
		AdminBaseURL:   "https://admin.fundis.co.ke",
	}
	msg := BuildApplicationMessage(e)

	if len(msg.Blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(msg.Blocks))
	}
	section := msg.Blocks[1]

	assertFieldContains(t, section.Fields, "Applicant ID", "<https://admin.fundis.co.ke/users/u-42|u-42>")
	assertFieldContains(t, section.Fields, "Vacancy ID", "<https://admin.fundis.co.ke/vacancies/v-99|v-99>")
}

func TestBuildApplicationMessage_WithoutAdminLinks(t *testing.T) {
	e := ApplicationEvent{
		ApplicantName:  "Jane Doe",
		ApplicantEmail: "jane@example.com",
		ApplicantPhone: "+254700000000",
		ApplicantID:    "u-42",
		VacancyID:      "v-99",
		VacancyTitle:   "Electrician",
		AdminBaseURL:   "",
	}
	msg := BuildApplicationMessage(e)

	if len(msg.Blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(msg.Blocks))
	}
	section := msg.Blocks[1]

	assertFieldContains(t, section.Fields, "Applicant ID", "u-42")
	assertFieldNotContains(t, section.Fields, "Applicant ID", "<")
	assertFieldContains(t, section.Fields, "Vacancy ID", "v-99")
	assertFieldNotContains(t, section.Fields, "Vacancy ID", "<")
}

func TestBuildRegistrationMessage_WithAdminLinks(t *testing.T) {
	e := RegistrationEvent{
		UserName:     "John Smith",
		UserEmail:    "john@example.com",
		UserPhone:    "+254711111111",
		UserID:       "u-77",
		UserType:     "artisan",
		AdminBaseURL: "https://admin.fundis.co.ke",
	}
	msg := BuildRegistrationMessage(e)

	if len(msg.Blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(msg.Blocks))
	}
	section := msg.Blocks[1]

	assertFieldContains(t, section.Fields, "User ID", "<https://admin.fundis.co.ke/users/u-77|u-77>")
}

func TestBuildRegistrationMessage_WithoutAdminLinks(t *testing.T) {
	e := RegistrationEvent{
		UserName:     "John Smith",
		UserEmail:    "john@example.com",
		UserPhone:    "+254711111111",
		UserID:       "u-77",
		UserType:     "artisan",
		AdminBaseURL: "",
	}
	msg := BuildRegistrationMessage(e)

	if len(msg.Blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(msg.Blocks))
	}
	section := msg.Blocks[1]

	assertFieldContains(t, section.Fields, "User ID", "u-77")
	assertFieldNotContains(t, section.Fields, "User ID", "<")
}

// --- test helpers ---

func assertFieldContains(t *testing.T, fields []TextObject, label, substr string) {
	t.Helper()
	for _, f := range fields {
		if strings.Contains(f.Text, "*"+label+":*") {
			if strings.Contains(f.Text, substr) {
				return
			}
			t.Fatalf("field %q text = %q, expected to contain %q", label, f.Text, substr)
		}
	}
	t.Fatalf("field %q not found in section fields", label)
}

func assertFieldNotContains(t *testing.T, fields []TextObject, label, substr string) {
	t.Helper()
	for _, f := range fields {
		if strings.Contains(f.Text, "*"+label+":*") {
			if strings.Contains(f.Text, substr) {
				t.Fatalf("field %q text = %q, expected NOT to contain %q", label, f.Text, substr)
			}
			return
		}
	}
	t.Fatalf("field %q not found in section fields", label)
}
