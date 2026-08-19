package workspaces

import (
	"strings"
	"testing"
)

// ─── ValidateName ───────────────────────────────────────────────────────────

func TestValidateName_Empty(t *testing.T) {
	err := ValidateName("")
	if err == nil {
		t.Fatal("expected error for empty string")
	}
	if !strings.Contains(err.Error(), "at least 2 characters") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestValidateName_SingleChar(t *testing.T) {
	err := ValidateName("A")
	if err == nil {
		t.Fatal("expected error for single character")
	}
	if !strings.Contains(err.Error(), "at least 2 characters") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestValidateName_TwoChars(t *testing.T) {
	if err := ValidateName("AB"); err != nil {
		t.Errorf("expected no error for 2-char name, got: %v", err)
	}
}

func TestValidateName_ExactlyEightyChars(t *testing.T) {
	name := strings.Repeat("a", 80)
	if err := ValidateName(name); err != nil {
		t.Errorf("expected no error for 80-char name, got: %v", err)
	}
}

func TestValidateName_EightyOneChars(t *testing.T) {
	name := strings.Repeat("a", 81)
	err := ValidateName(name)
	if err == nil {
		t.Fatal("expected error for 81-char name")
	}
	if !strings.Contains(err.Error(), "at most 80 characters") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestValidateName_OnlyPunctuation(t *testing.T) {
	err := ValidateName("---!!!")
	if err == nil {
		t.Fatal("expected error for punctuation-only name")
	}
	if !strings.Contains(err.Error(), "at least one letter or number") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestValidateName_WhitespaceOnly(t *testing.T) {
	err := ValidateName("   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only name")
	}
	if !strings.Contains(err.Error(), "at least 2 characters") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestValidateName_ValidNames(t *testing.T) {
	cases := []string{
		"Acme Corp",
		"日本語",
		"Ñoño",
		"12",
		"a1",
		"My Great Workspace!",
	}
	for _, name := range cases {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) unexpected error: %v", name, err)
		}
	}
}

func TestValidateName_TrimsBeforeCounting(t *testing.T) {
	// " a " trims to "a" which is 1 char — should fail
	err := ValidateName(" a ")
	if err == nil {
		t.Fatal("expected error for single char after trim")
	}
	// "  ab  " trims to "ab" which is 2 chars — should pass
	if err := ValidateName("  ab  "); err != nil {
		t.Errorf("expected no error for 2-char after trim, got: %v", err)
	}
}

func TestValidateName_MultibyteRunes(t *testing.T) {
	// Two CJK characters: each is one rune but multiple bytes
	name := "日本"
	if err := ValidateName(name); err != nil {
		t.Errorf("expected no error for 2-rune multibyte name, got: %v", err)
	}
}

// ─── NormalizeName ──────────────────────────────────────────────────────────

func TestNormalizeName_TrimsAndCollapses(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"  hello  world  ", "hello world"},
		{"no\t\textra\n\nspaces", "no extra spaces"},
		{"already clean", "already clean"},
		{"   ", ""},
		{"a", "a"},
		{"\t\n  Acme  \t Corp \n", "Acme Corp"},
	}
	for _, tc := range cases {
		got := NormalizeName(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ─── DeriveSlug ─────────────────────────────────────────────────────────────

func TestDeriveSlug_NormalNames(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Acme Corp", "acme-corp"},
		{"Hello World!", "hello-world"},
		{"my-workspace", "my-workspace"},
		{"Test 123", "test-123"},
		{"already-slug", "already-slug"},
	}
	for _, tc := range cases {
		got := DeriveSlug(tc.input)
		if got != tc.want {
			t.Errorf("DeriveSlug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDeriveSlug_EmojiOnly(t *testing.T) {
	got := DeriveSlug("🚀🎉")
	if got != "workspace" {
		t.Errorf("DeriveSlug(emoji-only) = %q, want %q", got, "workspace")
	}
}

func TestDeriveSlug_Arabic(t *testing.T) {
	// Arabic characters are not in [a-z0-9-], so they become a single hyphen
	// then get trimmed → "workspace"
	got := DeriveSlug("شركة")
	if got != "workspace" {
		t.Errorf("DeriveSlug(Arabic) = %q, want %q", got, "workspace")
	}
}

func TestDeriveSlug_PunctuationOnly(t *testing.T) {
	got := DeriveSlug("!@#$%^&*()")
	if got != "workspace" {
		t.Errorf("DeriveSlug(punctuation-only) = %q, want %q", got, "workspace")
	}
}

func TestDeriveSlug_WhitespaceOnly(t *testing.T) {
	got := DeriveSlug("   \t\n  ")
	if got != "workspace" {
		t.Errorf("DeriveSlug(whitespace-only) = %q, want %q", got, "workspace")
	}
}

func TestDeriveSlug_LeadingTrailingHyphens(t *testing.T) {
	// "---hello---" lowercased stays the same; the leading/trailing hyphens get trimmed
	got := DeriveSlug("---hello---")
	if got != "hello" {
		t.Errorf("DeriveSlug(---hello---) = %q, want %q", got, "hello")
	}
}

func TestDeriveSlug_NoRepeatedHyphens(t *testing.T) {
	// Multiple special characters in a row produce only one hyphen
	got := DeriveSlug("a   !!!   b")
	if got != "a-b" {
		t.Errorf("DeriveSlug(a   !!!   b) = %q, want %q", got, "a-b")
	}
}

func TestDeriveSlug_MixedScript(t *testing.T) {
	// "Café Délice" → "caf-d-lice" (é is outside a-z)
	got := DeriveSlug("Café Délice")
	if got != "caf-d-lice" {
		t.Errorf("DeriveSlug(Café Délice) = %q, want %q", got, "caf-d-lice")
	}
}
