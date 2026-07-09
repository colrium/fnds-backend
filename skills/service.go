package skills

import (
	"fmt"
	"strings"
)

func CreateSkillWithValidation(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	return CreateSkill(name)
}
