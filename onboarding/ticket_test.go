package onboarding

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestMintTicket_ProducesValidJWT(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-32bytes!!")

	stateID := "state-abc-123"
	stage := StageAwaitingWorkspaceName

	tokenStr, err := MintTicket(stateID, stage)
	if err != nil {
		t.Fatalf("MintTicket() error: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("MintTicket() returned empty token")
	}

	// Parse the token manually to verify claims.
	token, err := jwt.ParseWithClaims(tokenStr, &TicketClaims{}, func(token *jwt.Token) (any, error) {
		return []byte("test-ticket-secret-key-32bytes!!"), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		t.Fatalf("failed to parse minted token: %v", err)
	}

	claims, ok := token.Claims.(*TicketClaims)
	if !ok {
		t.Fatal("claims are not *TicketClaims")
	}

	// Verify state ID.
	if claims.StateID != stateID {
		t.Errorf("StateID = %q, want %q", claims.StateID, stateID)
	}

	// Verify stage.
	if claims.Stage != stage {
		t.Errorf("Stage = %q, want %q", claims.Stage, stage)
	}

	// Verify audience contains "fundis-onboarding".
	aud, _ := claims.GetAudience()
	hasAud := false
	for _, a := range aud {
		if a == "fundis-onboarding" {
			hasAud = true
			break
		}
	}
	if !hasAud {
		t.Errorf("Audience = %v, want to contain 'fundis-onboarding'", aud)
	}

	// Verify issuer is "fundis".
	if claims.Issuer != "fundis" {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, "fundis")
	}

	// Verify signing method is HS256.
	if token.Method != jwt.SigningMethodHS256 {
		t.Errorf("signing method = %v, want HS256", token.Method)
	}

	// Verify TTL is approximately 30 minutes.
	exp, _ := claims.GetExpirationTime()
	iat, _ := claims.GetIssuedAt()
	if exp == nil || iat == nil {
		t.Fatal("expected both exp and iat to be set")
	}
	ttl := exp.Sub(iat.Time)
	if ttl != 30*time.Minute {
		t.Errorf("TTL = %v, want 30m", ttl)
	}
}

func TestValidateTicket_AcceptsValidTicket(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-32bytes!!")

	stateID := "state-valid-456"
	stage := StageProvisioning

	tokenStr, err := MintTicket(stateID, stage)
	if err != nil {
		t.Fatalf("MintTicket() error: %v", err)
	}

	claims, err := ValidateTicket(tokenStr)
	if err != nil {
		t.Fatalf("ValidateTicket() error: %v", err)
	}

	if claims.StateID != stateID {
		t.Errorf("StateID = %q, want %q", claims.StateID, stateID)
	}
	if claims.Stage != stage {
		t.Errorf("Stage = %q, want %q", claims.Stage, stage)
	}
}

func TestValidateTicket_RejectsExpiredTicket(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-32bytes!!")

	// Create a token that expired 1 hour ago.
	past := time.Now().Add(-1 * time.Hour)
	claims := TicketClaims{
		StateID: "state-expired",
		Stage:   StageAwaitingWorkspaceName,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"fundis-onboarding"},
			Issuer:    "fundis",
			IssuedAt:  jwt.NewNumericDate(past.Add(-30 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(past),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte("test-ticket-secret-key-32bytes!!"))
	if err != nil {
		t.Fatalf("failed to sign expired token: %v", err)
	}

	_, err = ValidateTicket(tokenStr)
	if err == nil {
		t.Fatal("ValidateTicket() should reject an expired ticket")
	}
}

func TestValidateTicket_RejectsWrongAudience(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-32bytes!!")

	// Create a token with wrong audience.
	now := time.Now()
	claims := TicketClaims{
		StateID: "state-wrong-aud",
		Stage:   StageAwaitingWorkspaceName,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"wrong-audience"},
			Issuer:    "fundis",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Minute)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte("test-ticket-secret-key-32bytes!!"))
	if err != nil {
		t.Fatalf("failed to sign wrong-audience token: %v", err)
	}

	_, err = ValidateTicket(tokenStr)
	if err == nil {
		t.Fatal("ValidateTicket() should reject a ticket with wrong audience")
	}
}

func TestValidateTicket_RejectsWrongSigningKey(t *testing.T) {
	t.Setenv("SESSION_KEY", "test-ticket-secret-key-32bytes!!")

	// Create a token signed with a DIFFERENT key.
	now := time.Now()
	claims := TicketClaims{
		StateID: "state-wrong-key",
		Stage:   StageAwaitingWorkspaceName,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"fundis-onboarding"},
			Issuer:    "fundis",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Minute)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Sign with a completely different key.
	tokenStr, err := token.SignedString([]byte("completely-different-secret-key!"))
	if err != nil {
		t.Fatalf("failed to sign token with wrong key: %v", err)
	}

	_, err = ValidateTicket(tokenStr)
	if err == nil {
		t.Fatal("ValidateTicket() should reject a ticket signed with wrong key")
	}
}
