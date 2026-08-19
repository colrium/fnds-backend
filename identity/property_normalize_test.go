package identity

import (
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 3: Email_Normalization is idempotent and case-collapsing
// Validates: Requirements 1.1, 1.13, 4.1, 4.7, 10.2, 21.7
func TestProperty3_EmailNormalizationIdempotentAndCaseCollapsing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "s")

		// Idempotent.
		if got, want := NormalizeEmail(NormalizeEmail(s)), NormalizeEmail(s); got != want {
			t.Fatalf("NormalizeEmail is not idempotent: %q != %q", got, want)
		}
	})
}

// Feature: stytch-employer-onboarding, Property 3b: case and surrounding whitespace collapse to equal values
// Validates: Requirement 4.1
func TestProperty3_CaseAndWhitespaceVariantsNormalizeEqual(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.StringMatching(`[a-zA-Z0-9._%+-]{1,20}@[a-zA-Z0-9.-]{1,20}\.[a-zA-Z]{2,5}`).Draw(t, "base")
		leadingWS := rapid.StringOfN(rapid.SampledFrom([]rune{' ', '\t'}), 0, 3, -1).Draw(t, "leadingWS")
		trailingWS := rapid.StringOfN(rapid.SampledFrom([]rune{' ', '\t'}), 0, 3, -1).Draw(t, "trailingWS")

		variant := leadingWS + toRandomCase(t, base) + trailingWS
		if NormalizeEmail(variant) != NormalizeEmail(base) {
			t.Fatalf("case/whitespace variant %q did not normalize equal to %q: got %q vs %q",
				variant, base, NormalizeEmail(variant), NormalizeEmail(base))
		}
	})
}

// Feature: stytch-employer-onboarding, Property 3c: plus-suffix and dot-placement differences are NOT collapsed
// Validates: Requirement 4.7 (documented Gmail-alias limitation)
func TestProperty3_PlusSuffixAndDotPlacementYieldDifferentValues(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		local := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "local")
		domain := rapid.StringMatching(`[a-z]{3,10}\.[a-z]{2,5}`).Draw(t, "domain")
		tag := rapid.StringMatching(`[a-z]{1,8}`).Draw(t, "tag")

		base := local + "@" + domain
		plusSuffixed := local + "+" + tag + "@" + domain
		if NormalizeEmail(base) == NormalizeEmail(plusSuffixed) {
			t.Fatalf("plus-suffix variant unexpectedly normalized equal: %q vs %q", base, plusSuffixed)
		}

		if len(local) < 2 {
			return
		}
		dotted := local[:1] + "." + local[1:] + "@" + domain
		if NormalizeEmail(base) == NormalizeEmail(dotted) {
			t.Fatalf("dot-placement variant unexpectedly normalized equal: %q vs %q", base, dotted)
		}
	})
}

func toRandomCase(t *rapid.T, s string) string {
	flip := rapid.Bool().Draw(t, "flipCase")
	if !flip {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLower(r) {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
