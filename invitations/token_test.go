package invitations

import (
	"testing"
	"time"
)

func TestNewToken_GeneratesUniqueValues(t *testing.T) {
	pt1, h1, err1 := newToken()
	if err1 != nil {
		t.Fatalf("newToken() #1 error: %v", err1)
	}
	pt2, h2, err2 := newToken()
	if err2 != nil {
		t.Fatalf("newToken() #2 error: %v", err2)
	}

	if pt1 == pt2 {
		t.Errorf("expected different plaintexts, both are %q", pt1)
	}
	if h1 == h2 {
		t.Errorf("expected different hashes, both are %q", h1)
	}
}

func TestNewToken_HashMatchesHashToken(t *testing.T) {
	plaintext, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken() error: %v", err)
	}

	got := hashToken(plaintext)
	if got != hash {
		t.Errorf("hashToken(plaintext) = %q, want %q", got, hash)
	}
}

func TestNewToken_PlaintextLength(t *testing.T) {
	plaintext, _, err := newToken()
	if err != nil {
		t.Fatalf("newToken() error: %v", err)
	}

	// 32 bytes encoded in base64 raw URL = ceil(32*4/3) = 43 characters
	const want = 43
	if len(plaintext) != want {
		t.Errorf("plaintext length = %d, want %d", len(plaintext), want)
	}

	// Verify all characters are URL-safe base64 (A-Z, a-z, 0-9, -, _)
	for i, c := range plaintext {
		valid := (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_'
		if !valid {
			t.Errorf("plaintext[%d] = %q is not URL-safe base64", i, string(c))
		}
	}
}

func TestEffectiveStatus_PendingNotExpired(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(24 * time.Hour) // expires tomorrow

	got := EffectiveStatus(StatusPending, expiresAt, now)
	if got != StatusPending {
		t.Errorf("EffectiveStatus(pending, future) = %q, want %q", got, StatusPending)
	}
}

func TestEffectiveStatus_PendingExpired(t *testing.T) {
	now := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC) // expired a week ago

	got := EffectiveStatus(StatusPending, expiresAt, now)
	if got != StatusExpired {
		t.Errorf("EffectiveStatus(pending, past) = %q, want %q", got, StatusExpired)
	}
}

func TestEffectiveStatus_AcceptedStaysAccepted(t *testing.T) {
	now := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC) // past

	got := EffectiveStatus(StatusAccepted, expiresAt, now)
	if got != StatusAccepted {
		t.Errorf("EffectiveStatus(accepted, past) = %q, want %q", got, StatusAccepted)
	}
}

func TestEffectiveStatus_RevokedStaysRevoked(t *testing.T) {
	now := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC) // past

	got := EffectiveStatus(StatusRevoked, expiresAt, now)
	if got != StatusRevoked {
		t.Errorf("EffectiveStatus(revoked, past) = %q, want %q", got, StatusRevoked)
	}
}
