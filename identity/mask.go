package identity

import "strings"

// MaskEmail reveals at most the first character of the local part plus the full
// domain: "dennis@fundis.co.ke" -> "d***@fundis.co.ke", "a@b.c" -> "a***@b.c",
// "@b.c" -> "***@b.c". (Req 4.3)
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		// No '@' — treat the whole thing as a local part.
		if len(email) == 0 {
			return "***"
		}
		return string(email[0]) + "***"
	}

	local := email[:at]
	domain := email[at:] // includes the '@'

	if len(local) == 0 {
		return "***" + domain
	}
	return string(local[0]) + "***" + domain
}

// MaskPhone reveals at most the final 3 digits:
// "+254712345678" -> "*******678", "12" -> "12". (Req 4.3)
// MaskPhone("12") returning "12" is deliberate: "at most the final 3 digits"
// is satisfied, and padding would be a fabrication.
func MaskPhone(phone string) string {
	if len(phone) <= 3 {
		return phone
	}
	masked := len(phone) - 3
	return strings.Repeat("*", masked) + phone[masked:]
}
