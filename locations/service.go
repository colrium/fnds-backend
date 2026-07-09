package locations

import (
	"fmt"
	"strings"
)

func CreateLocationWithValidation(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("location name is required")
	}
	return CreateLocation(name)
}
