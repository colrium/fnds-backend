package courses

import (
	"testing"
)

func TestValidateCreateCourse(t *testing.T) {
	tests := []struct {
		name  string
		title string
		wantOk bool
	}{
		{"valid title", "Introduction to Plumbing", true},
		{"empty title", "", false},
		{"whitespace title", "   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateCourse(tt.title)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestValidateCreateTopic(t *testing.T) {
	tests := []struct {
		name  string
		title string
		wantOk bool
	}{
		{"valid title", "Pipe Fittings", true},
		{"empty title", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateTopic(tt.title)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestValidateCreateLesson(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		content string
		wantOk  bool
	}{
		{"valid lesson", "Lesson 1", "Content here", true},
		{"empty title", "", "Content here", false},
		{"empty content", "Lesson 1", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateLesson(tt.title, tt.content)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
