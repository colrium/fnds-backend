package jobs

import (
	"testing"
	"time"
)

func TestIsAfterHour(t *testing.T) {
	tests := []struct {
		name string
		hour int
		want bool
	}{
		{"6 AM", 6, true},
		{"8 AM", 8, true},
		{"12 PM", 12, true},
		{"9 PM", 21, true},
		{"10 PM", 22, false},
		{"5 AM", 5, false},
		{"midnight", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 9, tt.hour, 0, 0, 0, time.UTC)
			if got := IsAfterHour(now); got != tt.want {
				t.Errorf("IsAfterHour(%d) = %v, want %v", tt.hour, got, tt.want)
			}
		})
	}
}

func TestJobResults(t *testing.T) {
	result := SendJobRequestReminders()
	if !result.Success {
		t.Error("expected SendJobRequestReminders to succeed")
	}
	if result.Name != "SendJobRequestReminders" {
		t.Errorf("expected name 'SendJobRequestReminders', got %q", result.Name)
	}

	result = RequeryPendingTransactions()
	if !result.Success {
		t.Error("expected RequeryPendingTransactions to succeed")
	}
}
