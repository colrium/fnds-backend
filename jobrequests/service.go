package jobrequests

import (
	"fmt"
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

func ValidateCreateJobVacancy(title, description, location string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("description is required")
	}
	if strings.TrimSpace(location) == "" {
		return fmt.Errorf("location is required")
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
