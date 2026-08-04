package contextutil

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"backend/logging"
)

func newContext() echo.Context {
	app := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
	return app.NewContext(request, httptest.NewRecorder())
}

// SetUser must keep populating echo's store, which is what every existing
// caller reads through GetUserID, and additionally populate the request
// context, which is what the Better_Stack_Handler reads (Req 4.6).
func TestSetUserPopulatesEchoStoreAndRequestContext(t *testing.T) {
	c := newContext()
	const userID = "507f1f77bcf86cd799439011"

	SetUser(c, userID)

	if got := GetUserID(c); got != userID {
		t.Errorf("GetUserID() = %q, want %q", got, userID)
	}

	got, ok := logging.UserIDFromContext(c.Request().Context())
	if !ok {
		t.Fatal("UserIDFromContext reported ok == false after SetUser")
	}
	if got != userID {
		t.Errorf("UserIDFromContext() = %q, want %q", got, userID)
	}
}

// Before SetUser runs — every public route — the request context carries no user
// identifier, so the handler omits the user object (Req 4.10).
func TestRequestContextCarriesNoUserIDBeforeSetUser(t *testing.T) {
	c := newContext()

	if _, ok := logging.UserIDFromContext(c.Request().Context()); ok {
		t.Error("UserIDFromContext reported ok == true for a request that never authenticated")
	}
	if got := GetUserID(c); got != "" {
		t.Errorf("GetUserID() = %q, want empty string", got)
	}
}

// An empty user id must not be reported as an authenticated user (Req 4.10),
// while GetUserID keeps returning the empty string exactly as before.
func TestSetUserWithEmptyIDOmitsUserFromRequestContext(t *testing.T) {
	c := newContext()

	SetUser(c, "")

	if got := GetUserID(c); got != "" {
		t.Errorf("GetUserID() = %q, want empty string", got)
	}
	if _, ok := logging.UserIDFromContext(c.Request().Context()); ok {
		t.Error("UserIDFromContext reported ok == true for an empty user id")
	}
}

// SetUser replaces the request via c.SetRequest, so it must preserve the values
// the request-correlation middleware already attached upstream (Req 4.11).
func TestSetUserPreservesExistingRequestCorrelation(t *testing.T) {
	c := newContext()
	r := c.Request()
	c.SetRequest(r.WithContext(logging.ContextWithRequest(r.Context(), "req-1", http.MethodGet, "/api/v1/profile")))

	SetUser(c, "user-1")

	id, method, path, ok := logging.RequestFromContext(c.Request().Context())
	if !ok {
		t.Fatal("RequestFromContext reported ok == false after SetUser replaced the request")
	}
	if id != "req-1" || method != http.MethodGet || path != "/api/v1/profile" {
		t.Errorf("got (%q, %q, %q), want (\"req-1\", %q, \"/api/v1/profile\")", id, method, path, http.MethodGet)
	}
}

// The last SetUser call wins in both stores, so they can never disagree.
func TestSetUserOverwritesEarlierUserID(t *testing.T) {
	c := newContext()

	SetUser(c, "first")
	SetUser(c, "second")

	if got := GetUserID(c); got != "second" {
		t.Errorf("GetUserID() = %q, want %q", got, "second")
	}
	got, ok := logging.UserIDFromContext(c.Request().Context())
	if !ok || got != "second" {
		t.Errorf("UserIDFromContext() = (%q, %v), want (%q, true)", got, ok, "second")
	}
}
