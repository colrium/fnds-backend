package assessments

import (
	"testing"
)

func TestValidateSubmitResponse(t *testing.T) {
	tests := []struct {
		name         string
		assessmentID string
		answers      map[string]string
		wantOk       bool
	}{
		{"valid response", "assess-123", map[string]string{"q1": "a1"}, true},
		{"empty assessment ID", "", map[string]string{"q1": "a1"}, false},
		{"empty answers", "assess-123", map[string]string{}, false},
		{"nil answers", "assess-123", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubmitResponse(tt.assessmentID, tt.answers)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestValidateCreateAppointment(t *testing.T) {
	tests := []struct {
		name           string
		assessmentID   string
		scheduledDate  string
		scheduledTime  string
		wantOk         bool
	}{
		{"valid appointment", "assess-123", "2026-07-10", "10:00", true},
		{"empty assessment ID", "", "2026-07-10", "10:00", false},
		{"empty date", "assess-123", "", "10:00", false},
		{"empty time", "assess-123", "2026-07-10", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateAppointment(tt.assessmentID, tt.scheduledDate, tt.scheduledTime)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestValidateCreateTimeSlot(t *testing.T) {
	tests := []struct {
		name      string
		day       string
		startTime string
		endTime   string
		wantOk    bool
	}{
		{"valid slot", "monday", "09:00", "17:00", true},
		{"valid slot uppercase", "Monday", "09:00", "17:00", true},
		{"invalid day", "funday", "09:00", "17:00", false},
		{"empty day", "", "09:00", "17:00", false},
		{"empty start time", "monday", "", "17:00", false},
		{"empty end time", "monday", "09:00", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateTimeSlot(tt.day, tt.startTime, tt.endTime)
			if tt.wantOk && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
