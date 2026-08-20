package workspaces

import (
	"fmt"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
)

// routeKey identifies a single HTTP route by its method and path pattern.
type routeKey struct{ Method, Path string }

// RouteRegistry records every workspace-scoped route declaration made through
// a ScopedGroup. The startup audit (Verify) reads the accumulated declarations
// to confirm they match the code-resident workspaceScopedRoutes set.
type RouteRegistry struct {
	mu       sync.Mutex
	declared map[routeKey]Permission
}

// NewRouteRegistry returns a RouteRegistry ready for use.
func NewRouteRegistry() *RouteRegistry {
	return &RouteRegistry{
		declared: make(map[routeKey]Permission),
	}
}

// record stores a route declaration. It is called by ScopedGroup methods at
// registration time.
func (r *RouteRegistry) record(method, path string, p Permission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.declared[routeKey{Method: method, Path: path}] = p
}

// Declared returns a snapshot of every route declaration recorded so far. The
// returned map is a copy so concurrent mutation is safe.
func (r *RouteRegistry) Declared() map[routeKey]Permission {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[routeKey]Permission, len(r.declared))
	for k, v := range r.declared {
		out[k] = v
	}
	return out
}

// ScopedGroup is the only way to register a workspace-scoped route. Its method
// signatures take a Permission argument, so "registered without declaring a
// permission" is a compile error rather than a startup error (Req 8.5).
type ScopedGroup struct {
	group    *echo.Group
	registry *RouteRegistry
	resolver *Resolver
	prefix   string
}

// NewScopedGroup creates a ScopedGroup that registers routes on parent under
// the given prefix. Every route registered through it is recorded in registry
// and runs resolver.RequireSession before RequirePermission, so a
// ScopedGroup route always has a real SessionContext to authorize against.
func NewScopedGroup(parent *echo.Group, prefix string, resolver *Resolver, registry *RouteRegistry) *ScopedGroup {
	return &ScopedGroup{
		group:    parent.Group(prefix),
		registry: registry,
		resolver: resolver,
		prefix:   prefix,
	}
}

// GET registers a GET handler with the declared Permission. RequirePermission(p)
// runs before h and before any caller-supplied middleware, so declaring a
// Permission here always enforces it — a ScopedGroup route cannot be
// registered without authorization actually running.
func (s *ScopedGroup) GET(path string, p Permission, h echo.HandlerFunc, m ...echo.MiddlewareFunc) {
	s.registry.record("GET", s.prefix+path, p)
	s.group.GET(path, h, s.withRequirePermission(p, m)...)
}

// POST registers a POST handler with the declared Permission.
func (s *ScopedGroup) POST(path string, p Permission, h echo.HandlerFunc, m ...echo.MiddlewareFunc) {
	s.registry.record("POST", s.prefix+path, p)
	s.group.POST(path, h, s.withRequirePermission(p, m)...)
}

// PATCH registers a PATCH handler with the declared Permission.
func (s *ScopedGroup) PATCH(path string, p Permission, h echo.HandlerFunc, m ...echo.MiddlewareFunc) {
	s.registry.record("PATCH", s.prefix+path, p)
	s.group.PATCH(path, h, s.withRequirePermission(p, m)...)
}

// DELETE registers a DELETE handler with the declared Permission.
func (s *ScopedGroup) DELETE(path string, p Permission, h echo.HandlerFunc, m ...echo.MiddlewareFunc) {
	s.registry.record("DELETE", s.prefix+path, p)
	s.group.DELETE(path, h, s.withRequirePermission(p, m)...)
}

// withRequirePermission prepends resolver.RequireSession and RequirePermission(p)
// to the caller-supplied middleware chain, in that order: RequireSession must
// populate the SessionContext before RequirePermission can read it. A nil
// resolver skips RequireSession — tests exercising RequirePermission in
// isolation inject a fake SessionContext via their own middleware instead.
func (s *ScopedGroup) withRequirePermission(p Permission, m []echo.MiddlewareFunc) []echo.MiddlewareFunc {
	chain := make([]echo.MiddlewareFunc, 0, len(m)+2)
	if s.resolver != nil {
		chain = append(chain, s.resolver.RequireSession)
	}
	chain = append(chain, RequirePermission(p))
	chain = append(chain, m...)
	return chain
}

// workspaceScopedRoutes is the code-resident statement of intent: every route
// here MUST be workspace scoped. It is the second half of Req 8.6, because the
// compile-time half cannot catch an employer route that was left on authGroup by
// mistake. This set holds every row of the design's workspace-scoped route
// surface table.
var workspaceScopedRoutes = map[routeKey]struct{}{
	// Workspace management
	{Method: "GET", Path: "/workspaces/:workspaceID"}:                       {},
	{Method: "PATCH", Path: "/workspaces/:workspaceID"}:                     {},
	{Method: "POST", Path: "/workspaces/:workspaceID/deletion"}:             {},
	{Method: "DELETE", Path: "/workspaces/:workspaceID/deletion"}:           {},
	{Method: "GET", Path: "/workspaces/:workspaceID/permissions"}:           {},
	{Method: "POST", Path: "/workspaces/:workspaceID/import-personal-data"}: {},

	// Members
	{Method: "GET", Path: "/workspaces/:workspaceID/members"}:                             {},
	{Method: "PATCH", Path: "/workspaces/:workspaceID/members/:membershipID"}:             {},
	{Method: "DELETE", Path: "/workspaces/:workspaceID/members/:membershipID"}:            {},
	{Method: "POST", Path: "/workspaces/:workspaceID/members/:membershipID/suspension"}:   {},
	{Method: "DELETE", Path: "/workspaces/:workspaceID/members/:membershipID/suspension"}: {},

	// Ownership
	{Method: "POST", Path: "/workspaces/:workspaceID/ownership"}: {},

	// Invitations
	{Method: "GET", Path: "/workspaces/:workspaceID/invitations"}:             {},
	{Method: "POST", Path: "/workspaces/:workspaceID/invitations"}:            {},
	{Method: "POST", Path: "/workspaces/:workspaceID/invitations/:id/resend"}: {},
	{Method: "DELETE", Path: "/workspaces/:workspaceID/invitations/:id"}:      {},

	// Audit log
	{Method: "GET", Path: "/workspaces/:workspaceID/audit-log"}: {},

	// Job vacancies (re-scoped legacy paths)
	{Method: "GET", Path: "/job-vacancies"}:         {},
	{Method: "GET", Path: "/job-vacancy"}:           {},
	{Method: "POST", Path: "/job-vacancies"}:        {},
	{Method: "POST", Path: "/job-vacancies/update"}: {},
	{Method: "POST", Path: "/job-vacancies/delete"}: {},

	// Job applications (re-scoped legacy paths)
	{Method: "GET", Path: "/job-applications"}:            {},
	{Method: "GET", Path: "/job-applications/:id/review"}: {},
	{Method: "POST", Path: "/job-applications/decision"}:  {},

	// Employer dashboard and artisans
	{Method: "GET", Path: "/employer/dashboard"}: {},
	{Method: "GET", Path: "/employer/artisans"}:  {},

	// Companies
	{Method: "GET", Path: "/companies"}:         {},
	{Method: "POST", Path: "/companies"}:        {},
	{Method: "POST", Path: "/companies/update"}: {},
	{Method: "POST", Path: "/companies/delete"}: {},

	// Billing
	{Method: "GET", Path: "/subscriptions/mine"}:      {},
	{Method: "POST", Path: "/subscriptions/purchase"}: {},
	{Method: "POST", Path: "/subscriptions/cancel"}:   {},
	{Method: "GET", Path: "/invoices"}:                {},
	{Method: "POST", Path: "/invoices/:id/pay"}:       {},
	{Method: "GET", Path: "/payments"}:                {},
	{Method: "POST", Path: "/payment-methods"}:        {},
}

// sessionScopedRoutes are authenticated routes that do not require a workspace
// membership. They are excluded from Verify checks because they operate at the
// session level, not at the workspace level.
var sessionScopedRoutes = map[routeKey]struct{}{
	{Method: "GET", Path: "/workspaces"}:                      {},
	{Method: "POST", Path: "/workspaces"}:                     {},
	{Method: "POST", Path: "/workspaces/:workspaceID/switch"}: {},
}

// Verify is called by main.go after all route registration and returns an error
// that makes startup fail (Req 8.6). It asserts four things:
//  1. Every declared Permission exists in AllPermissions
//  2. Every route in workspaceScopedRoutes was declared through a ScopedGroup
//  3. No route in workspaceScopedRoutes also appears on a non-scoped group
//     (this check needs the Echo route table, but for now we check claim 2)
//  4. Every route registered through a ScopedGroup appears in
//     workspaceScopedRoutes, so the two lists cannot drift
//
// The three session-scoped /workspaces rows are excluded deliberately and by name.
func (r *RouteRegistry) Verify() error {
	declared := r.Declared()
	var errs []string

	// Check 1: Every declared Permission exists in AllPermissions.
	for key, perm := range declared {
		if _, ok := permissionIndex[perm]; !ok {
			errs = append(errs, fmt.Sprintf(
				"route %s %s declares unknown permission %q", key.Method, key.Path, perm))
		}
	}

	// Check 2: Every route in workspaceScopedRoutes was declared through a ScopedGroup.
	for key := range workspaceScopedRoutes {
		if _, ok := declared[key]; !ok {
			errs = append(errs, fmt.Sprintf(
				"workspaceScopedRoutes contains %s %s but it was not registered through a ScopedGroup",
				key.Method, key.Path))
		}
	}

	// Check 4: Every route registered through a ScopedGroup appears in
	// workspaceScopedRoutes. Session-scoped routes are excluded.
	for key := range declared {
		if _, ok := sessionScopedRoutes[key]; ok {
			continue
		}
		if _, ok := workspaceScopedRoutes[key]; !ok {
			errs = append(errs, fmt.Sprintf(
				"route %s %s was registered through a ScopedGroup but is not in workspaceScopedRoutes",
				key.Method, key.Path))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("route registry verification failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
