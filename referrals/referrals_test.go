package referrals

import (
	"strings"
	"testing"
)

func TestNormalizeCode(t *testing.T) {
	if got := NormalizeCode("  ab-c123  "); got != "AB-C123" {
		t.Fatalf("NormalizeCode() = %q, want %q", got, "AB-C123")
	}
}

func TestGenerateCode(t *testing.T) {
	code, err := GenerateCode()
	if err != nil {
		t.Fatalf("GenerateCode() error = %v", err)
	}
	if len(code) != CodeLength {
		t.Fatalf("GenerateCode() length = %d, want %d", len(code), CodeLength)
	}
	if code != strings.ToUpper(code) {
		t.Fatalf("GenerateCode() = %q, want uppercase", code)
	}
}

func TestReferralRewardIsThreeApplications(t *testing.T) {
	if RewardApplicationCredits != 3 {
		t.Fatalf("RewardApplicationCredits = %d, want 3", RewardApplicationCredits)
	}
}
