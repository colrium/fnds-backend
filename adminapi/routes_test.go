package adminapi

import (
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRegisterAdminRoutesRegistersUserEndpoints(t *testing.T) {
	e := echo.New()
	g := e.Group("/admin")
	RegisterAdminRoutes(g)

	want := map[string]bool{
		"GET /admin/users":              false,
		"GET /admin/users/:id":          false,
		"PATCH /admin/users/:id/status": false,
	}
	for _, r := range e.Routes() {
		key := r.Method + " " + r.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("route %q was not registered", route)
		}
	}
}
