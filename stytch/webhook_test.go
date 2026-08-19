package stytch

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// helper to create a valid signature for testing
func makeSignature(secret string, msgID, tsStr string, body []byte) string {
	// Strip whsec_ prefix and decode
	key, _ := base64.StdEncoding.DecodeString(secret[len("whsec_"):])
	signedContent := fmt.Sprintf("%s.%s.%s", msgID, tsStr, string(body))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signedContent))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return "v1," + sig
}

func TestVerifyWebhook_Success(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"member.created","data":{"member_id":"member-123"}}`)
	msgID := "msg_abc123"
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())

	sig := makeSignature(secret, msgID, tsStr, body)

	header := http.Header{}
	header.Set("svix-id", msgID)
	header.Set("svix-timestamp", tsStr)
	header.Set("svix-signature", sig)

	event, err := VerifyWebhook(secret, header, body, now)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if event.ID != msgID {
		t.Errorf("expected ID %q, got %q", msgID, event.ID)
	}
	if !event.Timestamp.Equal(time.Unix(now.Unix(), 0)) {
		t.Errorf("expected timestamp %v, got %v", time.Unix(now.Unix(), 0), event.Timestamp)
	}
	if string(event.Payload) != string(body) {
		t.Errorf("expected payload %q, got %q", string(body), string(event.Payload))
	}
}

func TestVerifyWebhook_MultipleSignatures(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"member.updated"}`)
	msgID := "msg_multi"
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())

	// Create the correct signature and add a wrong one before it
	correctSig := makeSignature(secret, msgID, tsStr, body)
	wrongSig := "v1," + base64.StdEncoding.EncodeToString([]byte("wrong-signature-bytes-here-1234"))

	header := http.Header{}
	header.Set("svix-id", msgID)
	header.Set("svix-timestamp", tsStr)
	header.Set("svix-signature", wrongSig+" "+correctSig)

	event, err := VerifyWebhook(secret, header, body, now)
	if err != nil {
		t.Fatalf("expected no error with multiple signatures, got: %v", err)
	}
	if event.ID != msgID {
		t.Errorf("expected ID %q, got %q", msgID, event.ID)
	}
}

func TestVerifyWebhook_MissingHeaders(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret"))
	body := []byte(`{}`)
	now := time.Now()

	tests := []struct {
		name    string
		headers map[string]string
		wantErr string
	}{
		{
			name:    "missing svix-id",
			headers: map[string]string{"svix-timestamp": "123", "svix-signature": "v1,abc"},
			wantErr: "missing svix-id header",
		},
		{
			name:    "missing svix-timestamp",
			headers: map[string]string{"svix-id": "msg_1", "svix-signature": "v1,abc"},
			wantErr: "missing svix-timestamp header",
		},
		{
			name:    "missing svix-signature",
			headers: map[string]string{"svix-id": "msg_1", "svix-timestamp": "123"},
			wantErr: "missing svix-signature header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for k, v := range tt.headers {
				header.Set(k, v)
			}
			_, err := VerifyWebhook(secret, header, body, now)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestVerifyWebhook_InvalidSecret(t *testing.T) {
	body := []byte(`{}`)
	now := time.Now()
	header := http.Header{}
	header.Set("svix-id", "msg_1")
	header.Set("svix-timestamp", fmt.Sprintf("%d", now.Unix()))
	header.Set("svix-signature", "v1,abc")

	tests := []struct {
		name    string
		secret  string
		wantErr string
	}{
		{
			name:    "no whsec_ prefix",
			secret:  "plain-secret",
			wantErr: "webhook secret must start with whsec_ prefix",
		},
		{
			name:    "invalid base64 after prefix",
			secret:  "whsec_not-valid-base64!!!",
			wantErr: "invalid base64 in webhook secret:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyWebhook(tt.secret, header, body, now)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Errorf("expected error starting with %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestVerifyWebhook_TimestampSkew(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"member.deleted"}`)
	msgID := "msg_old"
	now := time.Now()

	tests := []struct {
		name   string
		offset time.Duration
	}{
		{"too old (6 minutes in the past)", -6 * time.Minute},
		{"too new (6 minutes in the future)", 6 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventTime := now.Add(tt.offset)
			tsStr := fmt.Sprintf("%d", eventTime.Unix())
			sig := makeSignature(secret, msgID, tsStr, body)

			header := http.Header{}
			header.Set("svix-id", msgID)
			header.Set("svix-timestamp", tsStr)
			header.Set("svix-signature", sig)

			_, err := VerifyWebhook(secret, header, body, now)
			if err == nil {
				t.Fatal("expected error for timestamp skew, got nil")
			}
		})
	}
}

func TestVerifyWebhook_TimestampWithinTolerance(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"member.created"}`)
	msgID := "msg_near"
	now := time.Now()

	tests := []struct {
		name   string
		offset time.Duration
	}{
		{"4 minutes in the past", -4 * time.Minute},
		{"4 minutes in the future", 4 * time.Minute},
		{"exactly now", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventTime := now.Add(tt.offset)
			tsStr := fmt.Sprintf("%d", eventTime.Unix())
			sig := makeSignature(secret, msgID, tsStr, body)

			header := http.Header{}
			header.Set("svix-id", msgID)
			header.Set("svix-timestamp", tsStr)
			header.Set("svix-signature", sig)

			_, err := VerifyWebhook(secret, header, body, now)
			if err != nil {
				t.Fatalf("expected no error for timestamp within tolerance, got: %v", err)
			}
		})
	}
}

func TestVerifyWebhook_InvalidSignature(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"member.created"}`)
	msgID := "msg_badsig"
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())

	// Create a signature with a different body
	wrongSig := makeSignature(secret, msgID, tsStr, []byte("different-body"))

	header := http.Header{}
	header.Set("svix-id", msgID)
	header.Set("svix-timestamp", tsStr)
	header.Set("svix-signature", wrongSig)

	_, err := VerifyWebhook(secret, header, body, now)
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
	if err.Error() != "no matching signature found" {
		t.Errorf("expected 'no matching signature found', got %q", err.Error())
	}
}

func TestVerifyWebhook_TamperedBody(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	originalBody := []byte(`{"type":"member.created","data":{"member_id":"m123"}}`)
	msgID := "msg_tampered"
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())

	// Sign with the original body
	sig := makeSignature(secret, msgID, tsStr, originalBody)

	// Tamper the body
	tamperedBody := []byte(`{"type":"member.created","data":{"member_id":"HACKED"}}`)

	header := http.Header{}
	header.Set("svix-id", msgID)
	header.Set("svix-timestamp", tsStr)
	header.Set("svix-signature", sig)

	_, err := VerifyWebhook(secret, header, tamperedBody, now)
	if err == nil {
		t.Fatal("expected error for tampered body, got nil")
	}
}

func TestVerifyWebhook_InvalidTimestampFormat(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret"))
	body := []byte(`{}`)
	now := time.Now()

	header := http.Header{}
	header.Set("svix-id", "msg_1")
	header.Set("svix-timestamp", "not-a-number")
	header.Set("svix-signature", "v1,abc")

	_, err := VerifyWebhook(secret, header, body, now)
	if err == nil {
		t.Fatal("expected error for invalid timestamp, got nil")
	}
}

func TestVerifyWebhook_NoV1Prefix(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"test"}`)
	msgID := "msg_nov1"
	now := time.Now()
	tsStr := fmt.Sprintf("%d", now.Unix())

	// Build a correct HMAC but label it as v2 instead of v1
	key, _ := base64.StdEncoding.DecodeString(secret[len("whsec_"):])
	signedContent := fmt.Sprintf("%s.%s.%s", msgID, tsStr, string(body))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signedContent))
	sig := "v2," + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	header := http.Header{}
	header.Set("svix-id", msgID)
	header.Set("svix-timestamp", tsStr)
	header.Set("svix-signature", sig)

	_, err := VerifyWebhook(secret, header, body, now)
	if err == nil {
		t.Fatal("expected error when signature has no v1 prefix, got nil")
	}
}

func TestVerifyWebhook_BoundaryTimestamp(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-secret-key-1234567890"))
	body := []byte(`{"type":"boundary"}`)
	msgID := "msg_boundary"
	now := time.Now()

	// Exactly 5 minutes in the past should still be accepted
	eventTime := now.Add(-5 * time.Minute)
	tsStr := fmt.Sprintf("%d", eventTime.Unix())
	sig := makeSignature(secret, msgID, tsStr, body)

	header := http.Header{}
	header.Set("svix-id", msgID)
	header.Set("svix-timestamp", tsStr)
	header.Set("svix-signature", sig)

	_, err := VerifyWebhook(secret, header, body, now)
	if err != nil {
		t.Fatalf("expected no error at exactly 5 minutes, got: %v", err)
	}
}
