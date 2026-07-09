package insurance

import (
	"fmt"
	"strings"
)

func ValidateCreateInsurance(insuranceType, option string) error {
	validTypes := map[string]bool{
		"accident and last respect insurance": true,
		"health insurance":                    true,
		"business tools insurance":            true,
	}
	if strings.TrimSpace(insuranceType) == "" {
		return fmt.Errorf("insurance type is required")
	}
	if !validTypes[insuranceType] {
		return fmt.Errorf("invalid insurance type: must be one of accident and last respect insurance, health insurance, business tools insurance")
	}
	if strings.TrimSpace(option) == "" {
		return fmt.Errorf("option is required")
	}
	if option != "option 1" && option != "option 2" {
		return fmt.Errorf("invalid option: must be option 1 or option 2")
	}
	return nil
}

func ValidateBusinessToolsInsurance(totalToolValue float64) error {
	if totalToolValue <= 0 {
		return fmt.Errorf("total tool value must be greater than zero for business tools insurance")
	}
	return nil
}

func ValidateHealthInsurance(familySize int) error {
	if familySize <= 0 {
		return fmt.Errorf("family size must be greater than zero for health insurance")
	}
	return nil
}

func ValidateAccidentInsurance(nextOfKin string) error {
	if strings.TrimSpace(nextOfKin) == "" {
		return fmt.Errorf("next of kin is required for accident and last respect insurance")
	}
	return nil
}
