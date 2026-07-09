package institutions

import (
	"testing"
)

func TestCreateInstitutionValidation(t *testing.T) {
	tests := []struct {
		name     string
		instName string
		instType string
		wantErr  bool
		errMsg   string
	}{
		{"empty name", "", "university", true, "institution name is required"},
		{"empty type", "University of Rwanda", "", true, "institution type is required"},
		{"whitespace name", "   ", "university", true, "institution name is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CreateInstitutionWithValidation(tt.instName, tt.instType)
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

func TestUpdateInstitutionValidation(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		updates map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{"empty id", "", map[string]interface{}{"name": "New Name"}, true, "institution ID is required"},
		{"no updates", "abc123", map[string]interface{}{}, true, "no updates provided"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := UpdateInstitutionWithValidation(tt.id, tt.updates)
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
