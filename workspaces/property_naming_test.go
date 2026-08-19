package workspaces

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"pgregory.net/rapid"
)

// genWorkspaceName draws from several distinct shapes — emoji-only,
// Arabic-only, punctuation-only, whitespace-only, mixed-script, and
// ordinary ASCII — so DeriveSlug/ValidateName are exercised across the full
// range the design calls out.
func genWorkspaceName(t *rapid.T) string {
	kind := rapid.IntRange(0, 5).Draw(t, "nameKind")
	switch kind {
	case 0: // emoji-only
		return rapid.StringOfN(rapid.SampledFrom([]rune{'😀', '🎉', '🚀', '✨'}), 0, 20, -1).Draw(t, "name")
	case 1: // Arabic-only
		return rapid.StringOfN(rapid.SampledFrom([]rune{'ا', 'ب', 'ت', 'ث'}), 0, 20, -1).Draw(t, "name")
	case 2: // punctuation-only
		return rapid.StringOfN(rapid.SampledFrom([]rune{'!', '@', '#', '$', '%', '^', '&', '*'}), 0, 20, -1).Draw(t, "name")
	case 3: // whitespace-only
		return rapid.StringOfN(rapid.SampledFrom([]rune{' ', '\t', '\n'}), 0, 20, -1).Draw(t, "name")
	case 4: // mixed-script
		return rapid.StringOfN(rapid.SampledFrom([]rune{'a', 'ب', '1', '😀', '-', ' '}), 0, 30, -1).Draw(t, "name")
	default: // ordinary ASCII
		return rapid.StringMatching(`[A-Za-z0-9 _-]{0,40}`).Draw(t, "name")
	}
}

// Feature: stytch-employer-onboarding, Property 4: Workspace_Slug derivation is idempotent and yields a valid slug
// Validates: Requirements 2.7, 2.8, 2.9
func TestProperty4_SlugDerivationIdempotentAndValid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := genWorkspaceName(t)
		slug := DeriveSlug(name)

		if slug == "" {
			t.Fatalf("DeriveSlug(%q) returned empty string", name)
		}
		for _, r := range slug {
			if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				t.Fatalf("DeriveSlug(%q) = %q contains disallowed rune %q", name, slug, r)
			}
		}
		if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
			t.Fatalf("DeriveSlug(%q) = %q has a leading or trailing hyphen", name, slug)
		}
		if strings.Contains(slug, "--") {
			t.Fatalf("DeriveSlug(%q) = %q contains a repeated hyphen", name, slug)
		}
		if got, want := DeriveSlug(slug), DeriveSlug(name); got != want {
			t.Fatalf("DeriveSlug is not idempotent for %q: DeriveSlug(DeriveSlug(name))=%q, DeriveSlug(name)=%q", name, got, want)
		}
	})
}

// Feature: stytch-employer-onboarding, Property 5: Slug allocation never collides and never skips a suffix
// Validates: Requirements 2.8, 2.10
//
// Simulates the allocation algorithm in-memory (bare base slug, then -2, -3,
// ... on collision) rather than against Mongo, since the algorithm itself —
// not the database uniqueness enforcement — is what this property checks.
func TestProperty5_SlugAllocationNeverCollidesNeverSkips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Bias toward a small pool of names so many collide on the same base slug.
		pool := []string{"Acme", "Acme Inc", "ACME!!", "acme", "Zed Co", "Zed-Co"}
		names := rapid.SliceOfN(rapid.SampledFrom(pool), 1, 30).Draw(t, "names")

		held := make(map[string]bool)
		allocatedByBase := make(map[string][]string)

		for _, name := range names {
			base := DeriveSlug(name)
			var slug string
			for attempt := 1; attempt <= 100; attempt++ {
				candidate := base
				if attempt > 1 {
					candidate = fmt.Sprintf("%s-%d", base, attempt)
				}
				if !held[candidate] {
					slug = candidate
					break
				}
			}
			if slug == "" {
				t.Fatalf("exhausted 100 attempts allocating a slug for base %q", base)
			}
			if held[slug] {
				t.Fatalf("allocated slug %q collides with an already-held slug", slug)
			}
			held[slug] = true
			allocatedByBase[base] = append(allocatedByBase[base], slug)

			if !strings.HasPrefix(slug, base) {
				t.Fatalf("allocated slug %q does not start with its base %q", slug, base)
			}
		}

		// Every base slug's allocated sequence is base, base-2, base-3, ...
		// with no gap and no "-1".
		for base, slugs := range allocatedByBase {
			for i, slug := range slugs {
				var want string
				if i == 0 {
					want = base
				} else {
					want = fmt.Sprintf("%s-%d", base, i+1)
				}
				if slug != want {
					t.Fatalf("base %q: allocation #%d = %q, want %q (no gaps, no -1)", base, i+1, slug, want)
				}
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 6: Workspace_Name validation accepts exactly the specified range
// Validates: Requirements 2.2, 2.3, 2.4, 2.5
func TestProperty6_NameValidationAcceptsExactlySpecifiedRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := genWorkspaceName(t)

		trimmed := strings.TrimSpace(name)
		n := utf8.RuneCountInString(trimmed)
		hasLetterOrNumber := false
		for _, r := range trimmed {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				hasLetterOrNumber = true
				break
			}
		}
		wantValid := n >= 2 && n <= 80 && hasLetterOrNumber

		err := ValidateName(name)
		gotValid := err == nil
		if gotValid != wantValid {
			t.Fatalf("ValidateName(%q) valid=%v, want %v (trimmed len=%d, hasLetterOrNumber=%v)",
				name, gotValid, wantValid, n, hasLetterOrNumber)
		}
	})
}
