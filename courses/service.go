package courses

import (
	"fmt"
	"strings"
)

func ValidateCreateCourse(title string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("course title is required")
	}
	return nil
}

func ValidateCreateTopic(title string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("topic title is required")
	}
	return nil
}

func ValidateCreateLesson(title, content string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("lesson title is required")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("lesson content is required")
	}
	return nil
}
