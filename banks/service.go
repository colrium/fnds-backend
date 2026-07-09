package banks

import (
	"fmt"
	"strings"
)

func CreateBankWithValidation(name, code string) error {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	if name == "" {
		return fmt.Errorf("bank name is required")
	}
	if code == "" {
		return fmt.Errorf("bank code is required")
	}
	return CreateBank(name, code)
}
