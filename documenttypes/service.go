package documenttypes

import (
	"fmt"
	"strings"
)

func ValidateCreateDocumentType(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

func ValidateUpdateDocumentType(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}
