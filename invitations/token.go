package invitations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// newToken generates a cryptographically secure invitation token.
// Returns the plaintext (for the email/URL) and its SHA-256 hash (for storage).
// The plaintext exists only in the email and in the acceptance URL; the stored
// value is always the hash (Req 10.8, 20.12, 28).
func newToken() (plaintext string, hash string, err error) {
	// 32 bytes = 256 bits of entropy (exceeds the 128-bit minimum of Req 10.8)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(h[:])
	return plaintext, hash, nil
}

// hashToken computes the SHA-256 hash of a plaintext token for lookup.
func hashToken(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// EffectiveStatus is pure: a stored "pending" row whose expiry has passed reads
// as "expired" on every read, without waiting for the sweep (Req 12.7). The
// sweep only makes the stored value agree with the computed one, which is why
// P11's reference table has a clock-advance transition.
func EffectiveStatus(stored Status, expiresAt, now time.Time) Status {
	if stored == StatusPending && now.After(expiresAt) {
		return StatusExpired
	}
	return stored
}
