package chat

import (
	"fmt"
	"strings"
)

func ValidateCreateChat(message, chatType string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("message is required")
	}
	if len(message) > 255 {
		return fmt.Errorf("message must not exceed 255 characters")
	}
	validTypes := map[string]bool{"message": true, "schedule": true, "address": true}
	if !validTypes[chatType] {
		return fmt.Errorf("chat_type must be one of message, schedule, address")
	}
	return nil
}
