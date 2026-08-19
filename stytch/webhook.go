package stytch

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Event represents a parsed webhook event.
type Event struct {
	ID        string
	Type      string
	Timestamp time.Time
	Payload   []byte
}

// Stytch B2B webhook event types this feature handles. Any other value is
// recorded as processed and ignored with one info-level log, so an unknown
// or unsubscribed Stytch event never becomes a 500 retry loop.
const (
	EventMemberEmailUpdated  = "member.email.update"
	EventMemberDeleted       = "member.delete"
	EventMemberUpdated       = "member.update"
	EventOrganizationDeleted = "organization.delete"
)

// WebhookPayload is the subset of a Stytch B2B webhook body this feature
// reads, common across the member/organization event types above.
type WebhookPayload struct {
	OrganizationID string `json:"organization_id"`
	MemberID       string `json:"member_id"`
	Member         struct {
		MemberID string `json:"member_id"`
		Email    string `json:"email"`
	} `json:"member"`
}

// VerifyWebhook is a pure function that verifies a Stytch webhook delivery.
// It verifies the HMAC-SHA256 signature over "{svix-id}.{svix-timestamp}.{body}"
// compared with hmac.Equal, using the base64 secret after stripping the "whsec_" prefix.
// It rejects a timestamp more than 5 minutes from the passed-in `now`.
// Returns an Event on success, or an error on failure.
// Being pure (no server, no Svix library) is what makes P21 cheap.
func VerifyWebhook(secret string, header http.Header, rawBody []byte, now time.Time) (Event, error) {
	// 1. Extract headers
	msgID := header.Get("svix-id")
	if msgID == "" {
		return Event{}, errors.New("missing svix-id header")
	}

	tsStr := header.Get("svix-timestamp")
	if tsStr == "" {
		return Event{}, errors.New("missing svix-timestamp header")
	}

	sigHeader := header.Get("svix-signature")
	if sigHeader == "" {
		return Event{}, errors.New("missing svix-signature header")
	}

	// 2. Strip the "whsec_" prefix from the secret, then base64-decode it
	const prefix = "whsec_"
	if !strings.HasPrefix(secret, prefix) {
		return Event{}, errors.New("webhook secret must start with whsec_ prefix")
	}
	secretBytes, err := base64.StdEncoding.DecodeString(secret[len(prefix):])
	if err != nil {
		return Event{}, fmt.Errorf("invalid base64 in webhook secret: %w", err)
	}

	// 3. Parse the svix-timestamp as Unix seconds
	tsUnix, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return Event{}, fmt.Errorf("invalid svix-timestamp: %w", err)
	}
	eventTime := time.Unix(tsUnix, 0)

	// 4. Check timestamp is within 5 minutes of `now` (Req 19.3)
	// Comparison is on whole seconds since both sides are Unix-second precision.
	const tolerance = 5 * time.Minute
	diff := now.Unix() - tsUnix
	if diff > int64(tolerance.Seconds()) || diff < -int64(tolerance.Seconds()) {
		return Event{}, fmt.Errorf("timestamp skew too large: %ds", diff)
	}

	// 5. Build the signed content: "{svix-id}.{svix-timestamp}.{rawBody}"
	signedContent := fmt.Sprintf("%s.%s.%s", msgID, tsStr, string(rawBody))

	// 6. Compute HMAC-SHA256 of the signed content using the decoded secret key
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(signedContent))
	expectedMAC := mac.Sum(nil)

	// 7. The signature header may contain multiple comma-separated signatures
	// (each prefixed with "v1,"). Try each one.
	signatures := strings.Split(sigHeader, " ")
	verified := false
	for _, sig := range signatures {
		parts := strings.SplitN(sig, ",", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] != "v1" {
			continue
		}
		sigBytes, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		if hmac.Equal(sigBytes, expectedMAC) {
			verified = true
			break
		}
	}

	// 8. If none match, return an error
	if !verified {
		return Event{}, errors.New("no matching signature found")
	}

	// 9. On success, return an Event with the ID, timestamp, and raw payload.
	// The event type is read from the body's "type" field on a best-effort
	// basis: a missing or malformed type is not a verification failure (the
	// signature already proved authenticity), it just falls through to the
	// caller's unknown-event-type handling.
	var typed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawBody, &typed); err != nil {
		slog.Warn("stytch: webhook body is not valid JSON, event type unknown",
			"message_id", msgID,
			"error", err.Error(),
		)
	}

	return Event{
		ID:        msgID,
		Type:      typed.Type,
		Timestamp: eventTime,
		Payload:   rawBody,
	}, nil
}
