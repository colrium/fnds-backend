package audit

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 28: Secrets never leave the process
// Validates: Requirements 20.12, 23.7
//
// Scoped to the Audit_Logger's own redaction (redactSensitive), which is
// what Req 23.7 names directly — every Audit_Log_Entry must exclude every
// password hash, session token, Invitation_Token, and full payment
// instrument number. For an arbitrary secret value stored under an
// arbitrary sensitive-looking key, at arbitrary nesting depth inside a
// Previous/New payload, redactSensitive must remove it.
func TestProperty28_AuditRedactionRemovesSensitiveKeyedValues(t *testing.T) {
	sensitiveKeyBases := []string{"token", "secret", "password", "passwd", "authorization", "key", "session", "cookie", "jwt", "bearer"}

	rapid.Check(t, func(t *rapid.T) {
		secret := rapid.StringMatching(`[A-Za-z0-9+/_=-]{8,64}`).Draw(t, "secretValue")
		base := rapid.SampledFrom(sensitiveKeyBases).Draw(t, "keyBase")
		prefix := rapid.StringMatching(`[a-z_]{0,10}`).Draw(t, "keyPrefix")
		suffix := rapid.StringMatching(`[a-z_]{0,10}`).Draw(t, "keySuffix")
		sensitiveKey := prefix + base + suffix

		depth := rapid.IntRange(0, 4).Draw(t, "nestDepth")
		payload := map[string]any{sensitiveKey: secret}
		for i := 0; i < depth; i++ {
			payload = map[string]any{fmt.Sprintf("wrapper_%d", i): payload}
		}

		redacted := redactSensitive(payload)

		if containsValue(redacted, secret) {
			t.Fatalf("secret value %q survived redaction under key %q at depth %d: %#v", secret, sensitiveKey, depth, redacted)
		}
	})
}

// Feature: stytch-employer-onboarding, Property 28b: redaction is idempotent and never panics on arbitrary shapes
// Validates: Requirement 23.7
func TestProperty28_RedactionIdempotentOnArbitraryShapes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := genArbitraryMap(t, 3)
		first := redactSensitive(m)
		second := redactSensitive(first)

		if fmt.Sprintf("%#v", first) != fmt.Sprintf("%#v", second) {
			t.Fatalf("redactSensitive is not idempotent:\nfirst:  %#v\nsecond: %#v", first, second)
		}
	})
}

func genArbitraryMap(t *rapid.T, depth int) map[string]any {
	n := rapid.IntRange(0, 5).Draw(t, "fieldCount")
	m := make(map[string]any, n)
	for i := 0; i < n; i++ {
		key := rapid.StringMatching(`[a-zA-Z_]{1,15}`).Draw(t, "key")
		if depth > 0 && rapid.Bool().Draw(t, "nestFurther") {
			m[key] = genArbitraryMap(t, depth-1)
		} else {
			m[key] = rapid.StringMatching(`[a-zA-Z0-9]{0,20}`).Draw(t, "value")
		}
	}
	return m
}

// containsValue walks an arbitrary redacted structure looking for a string
// equal to want, at any depth.
func containsValue(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return t == want || strings.Contains(t, want)
	case map[string]any:
		for _, val := range t {
			if containsValue(val, want) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if containsValue(val, want) {
				return true
			}
		}
	}
	return false
}
