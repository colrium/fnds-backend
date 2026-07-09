package insurance

import "testing"

func TestValidateCreateInsurance(t *testing.T) {
	tests := []struct {
		name          string
		insuranceType string
		option        string
		wantErr       bool
		errMsg        string
	}{
		{"valid accident insurance", "accident and last respect insurance", "option 1", false, ""},
		{"valid health insurance", "health insurance", "option 2", false, ""},
		{"valid business tools", "business tools insurance", "option 1", false, ""},
		{"empty type", "", "option 1", true, "insurance type is required"},
		{"invalid type", "invalid", "option 1", true, "invalid insurance type: must be one of accident and last respect insurance, health insurance, business tools insurance"},
		{"empty option", "health insurance", "", true, "option is required"},
		{"invalid option", "health insurance", "option 3", true, "invalid option: must be option 1 or option 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateInsurance(tt.insuranceType, tt.option)
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

func TestValidateBusinessToolsInsurance(t *testing.T) {
	tests := []struct {
		name           string
		totalToolValue float64
		wantErr        bool
		errMsg         string
	}{
		{"valid", 50000, false, ""},
		{"zero", 0, true, "total tool value must be greater than zero for business tools insurance"},
		{"negative", -100, true, "total tool value must be greater than zero for business tools insurance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBusinessToolsInsurance(tt.totalToolValue)
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

func TestValidateHealthInsurance(t *testing.T) {
	tests := []struct {
		name       string
		familySize int
		wantErr    bool
		errMsg     string
	}{
		{"valid", 4, false, ""},
		{"zero", 0, true, "family size must be greater than zero for health insurance"},
		{"negative", -1, true, "family size must be greater than zero for health insurance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHealthInsurance(tt.familySize)
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

func TestValidateAccidentInsurance(t *testing.T) {
	tests := []struct {
		name      string
		nextOfKin string
		wantErr   bool
		errMsg    string
	}{
		{"valid", "Jane Doe", false, ""},
		{"empty", "", true, "next of kin is required for accident and last respect insurance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAccidentInsurance(tt.nextOfKin)
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
