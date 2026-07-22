package jobrequests

import (
	"testing"

	"backend/skills"
)

func TestWorkerVacancyFilter(t *testing.T) {
	t.Parallel()

	skillList := []skills.Skill{
		{ID: "skill-electrician", Name: "Electrician"},
		{ID: "skill-plumber", Name: "Plumber"},
	}

	tests := []struct {
		name          string
		profileSkills []string
		wantSkill     string
		wantMatch     bool
	}{
		{
			name:          "resolves selected skill id",
			profileSkills: []string{"skill-electrician"},
			wantSkill:     "Electrician",
			wantMatch:     true,
		},
		{
			name:          "supports legacy skill name",
			profileSkills: []string{"plumber"},
			wantSkill:     "Plumber",
			wantMatch:     true,
		},
		{
			name: "does not match without selected skill",
		},
		{
			name:          "does not match unknown skill",
			profileSkills: []string{"unknown"},
		},
		{
			name:          "does not choose arbitrarily from legacy multiple skills",
			profileSkills: []string{"skill-electrician", "skill-plumber"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			filter, skillName, matched := workerVacancyFilter(tt.profileSkills, skillList)
			if matched != tt.wantMatch {
				t.Fatalf("matched = %v, want %v", matched, tt.wantMatch)
			}
			if skillName != tt.wantSkill {
				t.Fatalf("skillName = %q, want %q", skillName, tt.wantSkill)
			}
			if !matched {
				return
			}
			if filter["status"] != "open" {
				t.Fatalf("status filter = %v, want open", filter["status"])
			}
			if filter["skill"] == nil {
				t.Fatal("skill filter is missing")
			}
		})
	}
}
