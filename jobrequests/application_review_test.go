package jobrequests

import (
	"encoding/json"
	"strings"
	"testing"

	"backend/profiles"
	"backend/users"
)

func TestBuildApplicantProfessionalProfile_CuratesJobRelevantFields(t *testing.T) {
	full := &profiles.FullProfile{
		User: &users.User{
			ID:        "worker-1",
			FirstName: "Amina",
			LastName:  "Otieno",
			Email:     "amina@example.com",
			Phone:     "+254700000000",
		},
		Avatar:                 "https://cdn.example/avatar.jpg",
		IDNumber:               "sensitive-id",
		City:                   "Nairobi",
		Country:                "Kenya",
		HighestEducationLevel:  "Diploma",
		Certificate:            "Electrical installation",
		Institution:            "institution-1",
		SkillAcquisitionMode:   "Apprenticeship",
		YearsOfExperience:      5,
		RecentGoodConduct:      true,
		NextOfKinName:          "Sensitive relative",
		Skills:                 []string{"skill-1"},
		Locations:              []string{"location-1"},
		PastEmployers:          []profiles.PastEmployerInput{{Name: "Grid Works", Location: "Nairobi"}},
		LanguageProficiencies:  []profiles.LanguageProficiencyInput{{Language: "English", ProficiencyLevel: "Fluent"}},
		WorkingHours:           []profiles.WorkingHourInput{{Day: "Monday", OpeningTime: "08:00", ClosingTime: "17:00"}},
		References:             []profiles.ReferenceInput{{Name: "Jane", JobTitle: "Supervisor"}},
		ProfessionalMembership: true,
		ProfessionalBody:       "EPRA",
		OtherQualifications:    "Solar installation",
		IsBusinessRegistered:   true,
		BusinessName:           "Amina Electricals",
	}
	vacancy := JobVacancy{Skill: "Electrician", Location: "Nairobi"}

	got := BuildApplicantProfessionalProfile(
		full,
		vacancy,
		ProfileLookupCatalog{
			SkillNames:       map[string]string{"skill-1": "Electrician"},
			LocationNames:    map[string]string{"location-1": "Nairobi"},
			InstitutionNames: map[string]string{"institution-1": "Technical University"},
		},
	)

	if !got.JobFit.SkillMatch {
		t.Fatal("expected applicant skill to match vacancy")
	}
	if !got.JobFit.LocationMatch {
		t.Fatal("expected applicant location to match vacancy")
	}
	if got.Institution != "Technical University" {
		t.Fatalf("Institution = %q, want resolved institution name", got.Institution)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	payload := string(encoded)
	for _, privateField := range []string{"id_number", "date_of_birth", "next_of_kin"} {
		if strings.Contains(payload, privateField) {
			t.Fatalf("professional profile leaked private field %q", privateField)
		}
	}
}

func TestCanReviewApplication_RequiresVacancyOwner(t *testing.T) {
	tests := []struct {
		name    string
		ownerID string
		vacancy *JobVacancy
		want    bool
	}{
		{
			name:    "vacancy owner",
			ownerID: "employer-1",
			vacancy: &JobVacancy{OwnerID: "employer-1"},
			want:    true,
		},
		{
			name:    "different employer",
			ownerID: "employer-2",
			vacancy: &JobVacancy{OwnerID: "employer-1"},
			want:    false,
		},
		{
			name:    "missing vacancy",
			ownerID: "employer-1",
			vacancy: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanReviewApplication(tt.ownerID, tt.vacancy); got != tt.want {
				t.Fatalf("CanReviewApplication() = %v, want %v", got, tt.want)
			}
		})
	}
}
