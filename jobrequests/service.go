package jobrequests

import (
	"fmt"
	"slices"
	"strings"
)

func ValidateCreateJobRequest(title, description string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("description is required")
	}
	return nil
}

// ValidSkills is the fixed set of trades an employer can select for a vacancy.
var ValidSkills = []string{
	"Mason",
	"Carpenter",
	"Electrician",
	"Plumber",
	"Painter",
	"Welder",
	"Tile Fixer",
	"Gardener",
	"Cleaner",
	"General Labourer",
}

func IsValidSkill(skill string) bool {
	return slices.Contains(ValidSkills, skill)
}

func ValidateCreateJobVacancy(title, description, location, skill string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("description is required")
	}
	if strings.TrimSpace(location) == "" {
		return fmt.Errorf("location is required")
	}
	if !IsValidSkill(skill) {
		return fmt.Errorf("skill is required and must be a valid trade")
	}
	return nil
}

func ValidateCreateJobApplication(vacancyID, coverLetter string) error {
	if strings.TrimSpace(vacancyID) == "" {
		return fmt.Errorf("vacancy ID is required")
	}
	if strings.TrimSpace(coverLetter) == "" {
		return fmt.Errorf("cover letter is required")
	}
	return nil
}
