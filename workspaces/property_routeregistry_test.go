package workspaces

import (
	"testing"

	"github.com/labstack/echo/v4"
	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 29: Every registered workspace-scoped endpoint declares a Permission
// Validates: Requirements 8.5, 8.6
//
// RouteRegistry.Verify enforces this invariant against the real,
// code-resident workspaceScopedRoutes table (exercised directly by
// TestWorkspaceScopedRoutesIsNonEmpty and friends in guard_test.go). This
// property instead fuzzes the registry/ScopedGroup machinery itself: for an
// arbitrary sequence of route declarations, every route recorded by a
// ScopedGroup maps to exactly one Permission drawn from the closed
// AllPermissions set (never a zero value, never an unknown value), matching
// the last declaration made for that (method, path) pair.
func TestProperty29_EveryDeclaredRouteHasExactlyOneKnownPermission(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := echo.New()
		reg := NewRouteRegistry()
		parent := e.Group("")
		sg := NewScopedGroup(parent, "/workspaces/:workspaceID", nil, reg)
		handler := func(c echo.Context) error { return nil }

		expected := make(map[routeKey]Permission)

		n := rapid.IntRange(0, 20).Draw(t, "routeCount")
		for i := 0; i < n; i++ {
			perm := rapid.SampledFrom(AllPermissions[:]).Draw(t, "perm")
			path := rapid.StringMatching(`/[a-z]{1,10}`).Draw(t, "path")
			method := rapid.SampledFrom([]string{"GET", "POST", "PATCH", "DELETE"}).Draw(t, "method")

			switch method {
			case "GET":
				sg.GET(path, perm, handler)
			case "POST":
				sg.POST(path, perm, handler)
			case "PATCH":
				sg.PATCH(path, perm, handler)
			case "DELETE":
				sg.DELETE(path, perm, handler)
			}
			expected[routeKey{Method: method, Path: "/workspaces/:workspaceID" + path}] = perm
		}

		declared := reg.Declared()

		if len(declared) != len(expected) {
			t.Fatalf("registry has %d declarations, want %d", len(declared), len(expected))
		}
		for key, wantPerm := range expected {
			gotPerm, ok := declared[key]
			if !ok {
				t.Fatalf("route %s %s was declared but is missing from the registry", key.Method, key.Path)
			}
			if gotPerm != wantPerm {
				t.Fatalf("route %s %s registry permission = %q, want %q (the last declared value)", key.Method, key.Path, gotPerm, wantPerm)
			}
			if !isKnownPermission(gotPerm) {
				t.Fatalf("route %s %s declares permission %q, not in AllPermissions", key.Method, key.Path, gotPerm)
			}
		}
	})
}

func isKnownPermission(p Permission) bool {
	for _, known := range AllPermissions {
		if p == known {
			return true
		}
	}
	return false
}
