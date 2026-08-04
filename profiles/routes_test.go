package profiles

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRegisterPublicRoutesRegistersCanonicalArtisanEndpoint(t *testing.T) {
	e := echo.New()
	RegisterPublicRoutes(e.Group("/api/v1"))

	assertRouteRegistered(t, e.Routes(), http.MethodGet, "/api/v1/artisans/:slug")
	assertRouteNotRegistered(t, e.Routes(), http.MethodGet, "/api/v1/profile/:userId")
}

func TestRegisterAuthRoutesRegistersVisibilityToggle(t *testing.T) {
	e := echo.New()
	RegisterAuthRoutes(e.Group("/api/v1"))

	assertRouteRegistered(t, e.Routes(), http.MethodPost, "/api/v1/profile/public")
}

func assertRouteRegistered(t *testing.T, routes []*echo.Route, method, path string) {
	t.Helper()
	for _, route := range routes {
		if route.Method == method && route.Path == path {
			return
		}
	}
	t.Fatalf("route %s %s is not registered", method, path)
}

func assertRouteNotRegistered(t *testing.T, routes []*echo.Route, method, path string) {
	t.Helper()
	for _, route := range routes {
		if route.Method == method && route.Path == path {
			t.Fatalf("route %s %s is unexpectedly registered", method, path)
		}
	}
}
