package reviews

import (
	"fmt"
	"strings"
)

func ValidateCreateReview(jobRequestID, artisanID string, rating int) error {
	if strings.TrimSpace(jobRequestID) == "" {
		return fmt.Errorf("job request ID is required")
	}
	if strings.TrimSpace(artisanID) == "" {
		return fmt.Errorf("artisan ID is required")
	}
	if rating < 1 || rating > 5 {
		return fmt.Errorf("rating must be between 1 and 5")
	}
	return nil
}
