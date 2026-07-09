package assessments

import (
	"fmt"
	"strings"
)

func ValidateSubmitResponse(assessmentID string, answers map[string]string) error {
	if strings.TrimSpace(assessmentID) == "" {
		return fmt.Errorf("assessment ID is required")
	}
	if len(answers) == 0 {
		return fmt.Errorf("answers are required")
	}
	return nil
}

func ValidateCreateAppointment(assessmentID, scheduledDate, scheduledTime string) error {
	if strings.TrimSpace(assessmentID) == "" {
		return fmt.Errorf("assessment ID is required")
	}
	if strings.TrimSpace(scheduledDate) == "" {
		return fmt.Errorf("scheduled date is required")
	}
	if strings.TrimSpace(scheduledTime) == "" {
		return fmt.Errorf("scheduled time is required")
	}
	return nil
}

func ValidateCreateTimeSlot(day, startTime, endTime string) error {
	validDays := map[string]bool{
		"monday": true, "tuesday": true, "wednesday": true,
		"thursday": true, "friday": true, "saturday": true, "sunday": true,
	}
	day = strings.ToLower(strings.TrimSpace(day))
	if !validDays[day] {
		return fmt.Errorf("invalid day: must be a valid day of the week")
	}
	if strings.TrimSpace(startTime) == "" {
		return fmt.Errorf("start time is required")
	}
	if strings.TrimSpace(endTime) == "" {
		return fmt.Errorf("end time is required")
	}
	return nil
}
