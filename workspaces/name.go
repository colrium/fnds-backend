package workspaces

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// slugNonAlnum matches runs of characters outside {a-z, 0-9} plus any
// hyphens immediately adjacent to such a run, so a disallowed character
// sitting next to a literal hyphen in the input collapses into that same
// hyphen instead of producing a repeated hyphen (e.g. "a" + non-ASCII + "-a"
// must not become "a--a").
var slugNonAlnum = regexp.MustCompile(`-*[^a-z0-9]+-*`)

// slugRepeatedHyphen collapses any remaining run of hyphens left after
// slugNonAlnum substitution — two literal hyphens already present in the
// input (e.g. "a--a") are themselves a run of disallowed-adjacent characters
// from the slug's perspective and must collapse to one.
var slugRepeatedHyphen = regexp.MustCompile(`-+`)

// whitespaceRun matches one or more whitespace characters.
var whitespaceRun = regexp.MustCompile(`\s+`)

// ValidateName accepts when the trimmed length is in the closed interval 2–80
// and the trimmed string holds at least one Unicode letter or number.
// Returns an error (suitable for a 400 response naming "workspace_name") on rejection.
// (Req 2.2, 2.3, 2.4, 2.5)
func ValidateName(raw string) error {
	trimmed := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(trimmed)

	if n < 2 {
		return fmt.Errorf("workspace_name must be at least 2 characters")
	}
	if n > 80 {
		return fmt.Errorf("workspace_name must be at most 80 characters")
	}

	hasLetterOrNumber := false
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			hasLetterOrNumber = true
			break
		}
	}
	if !hasLetterOrNumber {
		return fmt.Errorf("workspace_name must contain at least one letter or number")
	}

	return nil
}

// NormalizeName trims leading/trailing whitespace and collapses each run of
// consecutive whitespace characters to one space. (Req 2.6)
func NormalizeName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	return whitespaceRun.ReplaceAllString(trimmed, " ")
}

// DeriveSlug converts to lowercase, replaces each run of characters outside
// {a-z, 0-9, -} with one hyphen, strips leading and trailing hyphens, and
// returns "workspace" when the derivation empties. (Req 2.7, 2.9)
func DeriveSlug(name string) string {
	lower := strings.ToLower(name)
	slug := slugNonAlnum.ReplaceAllString(lower, "-")
	slug = slugRepeatedHyphen.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "workspace"
	}
	return slug
}
