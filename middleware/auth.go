package middleware

import (
	"net/http"
	"strings"

	"backend/contextutil"
	"backend/jwtutil"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
)

// SessionAuthenticator resolves a credential this package cannot validate on its
// own — a Stytch session JWT or opaque session token — to a Fundis user ID.
// Returns ok=false for anything it cannot authenticate.
//
// This exists as an injected hook rather than a direct call because the
// workspaces package imports this one (its admin group uses JWTAuth), so
// importing it back would be a cycle.
type SessionAuthenticator func(c echo.Context) (userID string, ok bool)

// stytchSessionAuth is nil until SetSessionAuthenticator runs, which is the
// correct state when employer Stytch auth is disabled: there are no Stytch
// sessions to accept, and JWTAuth behaves exactly as it always did.
var stytchSessionAuth SessionAuthenticator

// SetSessionAuthenticator registers the fallback credential path. Called once
// from main.go during startup, before the server begins serving.
func SetSessionAuthenticator(fn SessionAuthenticator) {
	stytchSessionAuth = fn
}

// resolveViaStytchSession consults the injected authenticator, treating an
// unset one as "cannot authenticate" so a deployment without employer Stytch
// auth needs no nil check at each call site.
func resolveViaStytchSession(c echo.Context) (string, bool) {
	if stytchSessionAuth == nil {
		return "", false
	}
	return stytchSessionAuth(c)
}

// JWTAuth guards the legacy authenticated routes.
//
// It validates a legacy HS256 token first — that is the credential every
// artisan, customer, assessor, and admin holds, and checking it costs no network
// call. Only when that fails does it try the Stytch chain, so an employer who
// signed in through Stytch can reach these routes too. Before that fallback
// existed, such an employer authenticated successfully and then got a 401 from
// every legacy route, which is every route the employer UI polls.
func JWTAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		authHeader := c.Request().Header.Get("Authorization")
		if authHeader == "" {
			return responses.SendError(c, http.StatusUnauthorized, "Authorization header required")
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			return responses.SendError(c, http.StatusUnauthorized, "Invalid authorization header format")
		}

		tokenString := parts[1]
		claims, err := jwtutil.ValidateToken(tokenString)
		if err != nil {
			// Not a legacy token. It may still be a Stytch session, which only
			// the workspaces resolver can verify.
			userID, ok := resolveViaStytchSession(c)
			if !ok {
				return responses.SendError(c, http.StatusUnauthorized, "Invalid or expired token")
			}
			// The resolver has already confirmed the user exists, is not
			// suspended, and holds an active membership, so re-loading them here
			// would only duplicate that work.
			contextutil.SetUser(c, userID)
			return next(c)
		}

		user, err := users.GetUserByID(c.Request().Context(), claims.UserID)
		if err != nil || user == nil {
			return responses.SendError(c, http.StatusUnauthorized, "User not found")
		}

		contextutil.SetUser(c, user.ID)
		return next(c)
	}
}
