package institutions

import (
	"fmt"
	"strings"
)

func CreateInstitutionWithValidation(name, instType string) (*Institution, error) {
	name = strings.TrimSpace(name)
	instType = strings.TrimSpace(instType)
	if name == "" {
		return nil, fmt.Errorf("institution name is required")
	}
	if instType == "" {
		return nil, fmt.Errorf("institution type is required")
	}
	inst := &Institution{
		Name: name,
		Type: instType,
	}
	if err := CreateInstitution(inst); err != nil {
		return nil, err
	}
	return inst, nil
}

func UpdateInstitutionWithValidation(id string, updates map[string]interface{}) error {
	if id == "" {
		return fmt.Errorf("institution ID is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}
	return UpdateInstitution(id, updates)
}
