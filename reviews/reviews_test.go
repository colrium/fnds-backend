package reviews

import (
	"testing"
)

func TestValidateCreateReview(t *testing.T) {
	tests := []struct {
		name        string
		jobRequestID string
		artisanID   string
		rating      int
		wantErr     bool
		errMsg      string
	}{
		{"valid review", "jr-123", "art-456", 4, false, ""},
		{"empty job request ID", "", "art-456", 4, true, "job request ID is required"},
		{"empty artisan ID", "jr-123", "", 4, true, "artisan ID is required"},
		{"rating too low", "jr-123", "art-456", 0, true, "rating must be between 1 and 5"},
		{"rating too high", "jr-123", "art-456", 6, true, "rating must be between 1 and 5"},
		{"minimum rating", "jr-123", "art-456", 1, false, ""},
		{"maximum rating", "jr-123", "art-456", 5, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateReview(tt.jobRequestID, tt.artisanID, tt.rating)
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
