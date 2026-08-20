package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
)

// Legacy authenticated routes validated an HS256 token and nothing else, so an
// employer signed in through Stytch was authenticated and then got a 401 from
// every one of them — notifications, profiles, quotations, everything the
// employer UI polls. SetSessionAuthenticator is the fallback that fixes it.
//
// The fallback path deliberately does not re-load the user (the resolver has
// already checked existence, status, and membership), which is what makes these
// tests possible without a database.

func setSessionAuthenticatorForTest(t *testing.T, fn SessionAuthenticator) {
	t.Helper()
	previous := stytchSessionAuth
	stytchSessionAuth = fn
	t.Cleanup(func() { stytchSessionAuth = previous })
}

// newAuthRequest builds a request carrying a token that is deliberately NOT a
// valid legacy HS256 JWT, so jwtutil rejects it and the fallback is reached.
func newAuthRequest(t *testing.T, token string) (echo.Context, *httptest.ResponseRecorder, *bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
	if token != "" {
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	reached := false
	return ctx, recorder, &reached
}

func TestJWTAuthAcceptsAStytchSession(t *testing.T) {
	t.Setenv("SESSION_KEY", "stytch-fallback-test-secret-key-32b")

	var sawRequest bool
	setSessionAuthenticatorForTest(t, func(echo.Context) (string, bool) {
		sawRequest = true
		return "employer-1", true
	})

	ctx, recorder, reached := newAuthRequest(t, "not.a.legacyjwt")
	handler := JWTAuth(func(c echo.Context) error {
		*reached = true
		// The downstream handler and every role guard read the user from the
		// context, so the fallback must populate it exactly as the legacy path does.
		if got := contextutil.GetUserID(c); got != "employer-1" {
			t.Errorf("context user ID = %q, want %q", got, "employer-1")
		}
		return c.NoContent(http.StatusOK)
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !sawRequest {
		t.Error("the session authenticator was never consulted")
	}
	if !*reached {
		t.Errorf("request was rejected with %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

func TestJWTAuthRejectsWhenTheStytchSessionDoesNotResolve(t *testing.T) {
	t.Setenv("SESSION_KEY", "stytch-fallback-test-secret-key-32b")

	setSessionAuthenticatorForTest(t, func(echo.Context) (string, bool) {
		return "", false
	})

	ctx, recorder, reached := newAuthRequest(t, "not.a.legacyjwt")
	handler := JWTAuth(func(c echo.Context) error {
		*reached = true
		return c.NoContent(http.StatusOK)
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if *reached {
		t.Error("an unresolvable credential must not reach the handler")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}

// With employer Stytch auth disabled nothing registers an authenticator, so the
// nil case is the normal state for such a deployment, not an edge case. Without
// the guard every bad token would panic instead of returning 401.
func TestJWTAuthWithNoAuthenticatorRegistered(t *testing.T) {
	t.Setenv("SESSION_KEY", "stytch-fallback-test-secret-key-32b")

	setSessionAuthenticatorForTest(t, nil)

	ctx, recorder, reached := newAuthRequest(t, "not.a.legacyjwt")
	handler := JWTAuth(func(c echo.Context) error {
		*reached = true
		return c.NoContent(http.StatusOK)
	})

	if err := handler(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if *reached {
		t.Error("handler must not be reached without any valid credential")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}

// A missing header must still fail before either credential path runs, so the
// fallback cannot be used to bypass the header requirement.
func TestJWTAuthStillRequiresTheHeader(t *testing.T) {
	var consulted bool
	setSessionAuthenticatorForTest(t, func(echo.Context) (string, bool) {
		consulted = true
		return "employer-1", true
	})

	ctx, recorder, _ := newAuthRequest(t, "")
	handler := JWTAuth(func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	if err := handler(ctx); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if consulted {
		t.Error("the authenticator must not be consulted when no header was sent")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}
