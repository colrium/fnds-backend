package identity

import "strings"

// NormalizeEmail applies Email_Normalization: trim whitespace then lowercase.
// This is the ONE function used identically by Onboarding_Service,
// Invitation_Service, and Migration_Job. (Req 1.1, 1.13, 4.1, 4.7, 10.2)
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
