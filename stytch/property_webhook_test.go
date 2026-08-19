package stytch

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// flipOneByte mutates a single byte of a copy of b at a valid index, if b is
// non-empty. Returns the mutated copy and whether a mutation actually
// happened (b may be empty).
func flipOneByte(b []byte, idx int) ([]byte, bool) {
	if len(b) == 0 {
		return b, false
	}
	out := make([]byte, len(b))
	copy(out, b)
	i := idx % len(out)
	out[i] ^= 0xFF
	return out, true
}

// Feature: stytch-employer-onboarding, Property 21: Webhook signature verification rejects every tampered payload
// Validates: Requirements 19.1, 19.2, 19.3
func TestProperty21_WebhookSignatureVerificationRejectsEveryTamperedPayload(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
		bodyStr := rapid.StringMatching(`\{"type":"[a-z.]{3,20}","data":\{"member_id":"m-[0-9]{1,5}"\}\}`).Draw(t, "body")
		body := []byte(bodyStr)
		msgID := "msg_" + rapid.StringMatching(`[a-zA-Z0-9]{6,12}`).Draw(t, "msgID")
		now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
		tsStr := fmt.Sprintf("%d", now.Unix())

		sig := makeSignature(secret, msgID, tsStr, body)

		validHeader := func() http.Header {
			h := http.Header{}
			h.Set("svix-id", msgID)
			h.Set("svix-timestamp", tsStr)
			h.Set("svix-signature", sig)
			return h
		}

		// Sanity: the unmutated payload verifies successfully.
		if _, err := VerifyWebhook(secret, validHeader(), body, now); err != nil {
			t.Fatalf("unmutated payload failed to verify: %v", err)
		}

		target := rapid.SampledFrom([]string{"body", "signature", "timestamp"}).Draw(t, "mutationTarget")
		idx := rapid.IntRange(0, 1<<20).Draw(t, "byteIndex")

		switch target {
		case "body":
			mutated, ok := flipOneByte(body, idx)
			if !ok {
				return // empty body, nothing to mutate
			}
			if string(mutated) == string(body) {
				return // XOR with 0xFF on the same byte value can't be a no-op, but guard anyway
			}
			_, err := VerifyWebhook(secret, validHeader(), mutated, now)
			if err == nil {
				t.Fatalf("tampered body verified successfully: body=%q mutated=%q", body, mutated)
			}
		case "signature":
			sigBytes := []byte(sig)
			mutated, ok := flipOneByte(sigBytes, idx)
			if !ok {
				return
			}
			h := validHeader()
			h.Set("svix-signature", string(mutated))
			_, err := VerifyWebhook(secret, h, body, now)
			if err == nil {
				t.Fatalf("tampered signature verified successfully: sig=%q mutated=%q", sig, mutated)
			}
		case "timestamp":
			tsBytes := []byte(tsStr)
			mutated, ok := flipOneByte(tsBytes, idx)
			if !ok {
				return
			}
			if string(mutated) == tsStr {
				return
			}
			h := http.Header{}
			h.Set("svix-id", msgID)
			h.Set("svix-timestamp", string(mutated))
			h.Set("svix-signature", sig) // signature was computed over the ORIGINAL timestamp
			_, err := VerifyWebhook(secret, h, body, now)
			if err == nil {
				t.Fatalf("tampered timestamp header verified successfully: ts=%q mutated=%q", tsStr, mutated)
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 21b: a timestamp more than five minutes from now is always rejected, even with a valid signature over it
// Validates: Requirement 19.3
func TestProperty21_TimestampSkewBeyondFiveMinutesAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
		body := []byte(`{"type":"member.update","data":{"member_id":"m-1"}}`)
		msgID := "msg_skew"
		now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

		skewSeconds := rapid.IntRange(301, 1_000_000).Draw(t, "skewSeconds")
		negative := rapid.Bool().Draw(t, "negative")
		if negative {
			skewSeconds = -skewSeconds
		}
		skewedTime := now.Add(time.Duration(skewSeconds) * time.Second)
		tsStr := fmt.Sprintf("%d", skewedTime.Unix())

		// Signature is validly computed over the skewed timestamp — the
		// signature itself is not what should cause rejection here.
		sig := makeSignature(secret, msgID, tsStr, body)
		h := http.Header{}
		h.Set("svix-id", msgID)
		h.Set("svix-timestamp", tsStr)
		h.Set("svix-signature", sig)

		_, err := VerifyWebhook(secret, h, body, now)
		if err == nil {
			t.Fatalf("timestamp skewed by %ds from backend clock verified successfully", skewSeconds)
		}
	})
}
