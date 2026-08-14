package auth

import (
	"testing"

	"github.com/labstack/echo/v4"
)

// Regression test for the bug where POST /admin/users was registered on the
// plain JWT-authenticated group instead of the IsAdmin-guarded group, letting
// any authenticated user create accounts (including, previously, admins).
func TestAdminUserCreationIsNotOnThePlainAuthGroup(t *testing.T) {
	e := echo.New()
	g := e.Group("")
	RegisterAuthRoutes(g)

	for _, r := range e.Routes() {
		if r.Method == "POST" && r.Path == "/admin/users" {
			t.Fatalf("POST /admin/users must not be registered by RegisterAuthRoutes (no IsAdmin guard)")
		}
	}
}

func TestAdminUserCreationIsRegisteredOnAdminRoutes(t *testing.T) {
	e := echo.New()
	g := e.Group("/admin")
	RegisterAdminRoutes(g)

	found := false
	for _, r := range e.Routes() {
		if r.Method == "POST" && r.Path == "/admin/users" {
			found = true
		}
	}
	if !found {
		t.Fatal("POST /admin/users must be registered by RegisterAdminRoutes")
	}
}
