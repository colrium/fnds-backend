package profiles

import (
	"testing"

	"backend/users"
)

func TestGetProfileCompletion(t *testing.T) {
	t.Parallel()

	completeProfile := &FullProfile{
		User:                 &users.User{},
		IDNumber:             "12345678",
		DateOfBirth:          "1990-01-01",
		Gender:               "female",
		Address:              "Nairobi",
		Skills:               []string{"skill-1"},
		YearsOfExperience:    0,
		SkillAcquisitionMode: "apprenticeship",
		References: []ReferenceInput{{
			Name:        "Reference",
			PhoneNumber: "+254700000000",
		}},
		NextOfKinName:         "Kin",
		NextOfKinPhoneNumber:  "+254711111111",
		NextOfKinRelationship: "Sibling",
		Locations:             []string{"location-1"},
		WorkingHours: []WorkingHourInput{{
			Day:         "Monday",
			OpeningTime: "08:00",
			ClosingTime: "17:00",
		}},
	}

	tests := []struct {
		name            string
		profile         *FullProfile
		wantComplete    bool
		wantPercent     int
		wantMissingPart string
	}{
		{
			name:         "all six sections complete",
			profile:      completeProfile,
			wantComplete: true,
			wantPercent:  100,
		},
		{
			name: "missing skill leaves profile incomplete",
			profile: func() *FullProfile {
				profile := *completeProfile
				profile.Skills = nil
				return &profile
			}(),
			wantPercent:     83,
			wantMissingPart: "Skills and experience",
		},
		{
			name:            "empty profile has no completed sections",
			profile:         &FullProfile{User: &users.User{}},
			wantPercent:     0,
			wantMissingPart: "Personal details",
		},
		{
			name: "rounds one complete section like the frontend",
			profile: &FullProfile{
				User:      &users.User{},
				Locations: []string{"location-1"},
			},
			wantPercent:     17,
			wantMissingPart: "Personal details",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := GetProfileCompletion(tt.profile)
			if got.Complete != tt.wantComplete {
				t.Fatalf("Complete = %v, want %v", got.Complete, tt.wantComplete)
			}
			if got.Percent != tt.wantPercent {
				t.Fatalf("Percent = %d, want %d", got.Percent, tt.wantPercent)
			}
			if tt.wantMissingPart != "" && !containsString(got.MissingSections, tt.wantMissingPart) {
				t.Fatalf("MissingSections = %v, want %q", got.MissingSections, tt.wantMissingPart)
			}
		})
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
