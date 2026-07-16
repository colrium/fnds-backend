package jobrequests

import (
	"testing"
)

func TestValidateCreateJobRequest(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		wantErr     bool
		errMsg      string
	}{
		{"valid request", "Fix plumbing", "Need pipe repair", false, ""},
		{"empty title", "", "Need pipe repair", true, "title is required"},
		{"empty description", "Fix plumbing", "", true, "description is required"},
		{"whitespace title", "   ", "Need pipe repair", true, "title is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateJobRequest(tt.title, tt.description)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateCreateJobVacancy(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		location    string
		skill       string
		wantErr     bool
		errMsg      string
	}{
		{"valid vacancy", "Electrician needed", "Need electrician", "Kigali", "Electrician", false, ""},
		{"empty title", "", "Need electrician", "Kigali", "Electrician", true, "title is required"},
		{"empty description", "Electrician needed", "", "Kigali", "Electrician", true, "description is required"},
		{"empty location", "Electrician needed", "Need electrician", "", "Electrician", true, "location is required"},
		{"empty skill", "Electrician needed", "Need electrician", "Kigali", "", true, "skill is required and must be a valid trade"},
		{"invalid skill", "Electrician needed", "Need electrician", "Kigali", "Astronaut", true, "skill is required and must be a valid trade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateJobVacancy(tt.title, tt.description, tt.location, tt.skill)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateCreateJobApplication(t *testing.T) {
	tests := []struct {
		name        string
		vacancyID   string
		coverLetter string
		wantErr     bool
		errMsg      string
	}{
		{"valid application", "vac-123", "I am interested", false, ""},
		{"empty vacancy ID", "", "I am interested", true, "vacancy ID is required"},
		{"empty cover letter", "vac-123", "", true, "cover letter is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateJobApplication(tt.vacancyID, tt.coverLetter)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
