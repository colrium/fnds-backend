package documenttypes

import "testing"

func TestValidateCreateDocumentType(t *testing.T) {
	tests := []struct {
		name    string
		dtName  string
		wantErr bool
		errMsg  string
	}{
		{"valid", "National ID", false, ""},
		{"empty", "", true, "name is required"},
		{"whitespace", "   ", true, "name is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateDocumentType(tt.dtName)
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

func TestValidateUpdateDocumentType(t *testing.T) {
	tests := []struct {
		name    string
		dtName  string
		wantErr bool
		errMsg  string
	}{
		{"valid", "Passport", false, ""},
		{"empty", "", true, "name is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUpdateDocumentType(tt.dtName)
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
