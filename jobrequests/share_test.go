package jobrequests

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

func TestVacancySharePageRenders(t *testing.T) {
	data := vacancyShareData{
		Title: "Electrician", Company: "Acme & Co", Description: "Wire a building",
		MetaDesc: "Wire a building", CanonicalURL: "https://api.example.com/vacancy/1",
		ApplyURL: "https://app.example.com/vacancy/1", Monogram: "AC", StatusOpen: true,
		Fields: []shareField{{"Skill", "Electrician"}},
		JSONLD: template.JS(`{"@type":"JobPosting"}`),
	}
	var buf bytes.Buffer
	if err := vacancySharePage.Execute(&buf, data); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()
	// User-supplied company name with & must be HTML-escaped (XSS safety).
	if !strings.Contains(out, "Acme &amp; Co") {
		t.Errorf("expected escaped company name; got %s", out)
	}
	if !strings.Contains(out, `href="https://app.example.com/vacancy/1"`) {
		t.Error("apply URL missing")
	}
	if !strings.Contains(out, `{"@type":"JobPosting"}`) {
		t.Error("JSON-LD missing")
	}
}

func TestSchemaEmploymentType(t *testing.T) {
	cases := map[string]string{
		"FULL_TIME":  "FULL_TIME",
		"PART_TIME":  "PART_TIME",
		"CONTRACT":   "CONTRACTOR",
		"INTERNSHIP": "INTERN",
		"weird":      "",
		"":           "",
	}
	for in, want := range cases {
		if got := schemaEmploymentType(in); got != want {
			t.Errorf("schemaEmploymentType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildJobPostingJSONLD(t *testing.T) {
	v := &JobVacancy{
		Title:              "Electrician",
		Description:        "Wire a building",
		EmploymentType:     "FULL_TIME",
		Location:           "Nairobi",
		ApplicationEndDate: "2026-12-31T00:00:00Z",
		CreatedAt:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	out := buildJobPostingJSONLD(v, "Acme Ltd")
	for _, want := range []string{"JobPosting", "Electrician", "Acme Ltd", "FULL_TIME", "Nairobi", "\"addressCountry\":\"KE\"", "validThrough"} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON-LD missing %q; got %s", want, out)
		}
	}
}

func TestFmtShareDate(t *testing.T) {
	if got := fmtShareDate(""); got != "N/A" {
		t.Errorf("empty date = %q, want N/A", got)
	}
	if got := fmtShareDate("2026-12-31T00:00:00Z"); got != "Dec 31, 2026" {
		t.Errorf("date = %q, want Dec 31, 2026", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x"); got != "x" {
		t.Errorf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty all empty = %q, want empty", got)
	}
}
