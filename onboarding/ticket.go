package onboarding

import (
	"fmt"
	"time"

	"backend/configs"

	"github.com/golang-jwt/jwt/v5"
)

// minSessionKeyBytes is the minimum acceptable length for the HS256 signing
// key. Below this, a signature can be forged with realistic compute.
const minSessionKeyBytes = 32

// TicketClaims is the JWT claims for the onboarding ticket.
// It carries a distinct audience ["fundis-onboarding"] so it can never be
// mistaken for a jwtutil access token. TTL 30 minutes.
type TicketClaims struct {
	StateID string `json:"sid"`
	Stage   Stage  `json:"stg"`
	jwt.RegisteredClaims
}

// MintTicket creates an HS256 JWT signed with configs.GetSessionKey() carrying
// the onboarding state ID and current stage. Audience is ["fundis-onboarding"],
// Issuer is "fundis", TTL is 30 minutes.
//
// The intermediate_session_token never leaves the server — this ticket is the
// only bearer value sent to the client during onboarding.
func MintTicket(stateID string, stage Stage) (string, error) {
	now := time.Now()
	claims := TicketClaims{
		StateID: stateID,
		Stage:   stage,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"fundis-onboarding"},
			Issuer:    "fundis",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Minute)),
		},
	}

	key := []byte(configs.GetSessionKey())
	if len(key) < minSessionKeyBytes {
		return "", fmt.Errorf("onboarding: session key too short: need at least %d bytes", minSessionKeyBytes)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(key)
}

// ValidateTicket parses and validates an onboarding ticket JWT.
// Returns the claims if the token is valid with the correct audience.
func ValidateTicket(tokenStr string) (*TicketClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &TicketClaims{}, func(token *jwt.Token) (any, error) {
		return []byte(configs.GetSessionKey()), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*TicketClaims)
	if !ok || !token.Valid {
		return nil, jwt.ErrTokenInvalidClaims
	}

	// Verify audience contains "fundis-onboarding"
	aud, _ := claims.GetAudience()
	hasAud := false
	for _, a := range aud {
		if a == "fundis-onboarding" {
			hasAud = true
			break
		}
	}
	if !hasAud {
		return nil, jwt.ErrTokenInvalidAudience
	}

	return claims, nil
}
