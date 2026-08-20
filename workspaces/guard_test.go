package workspaces

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestNewRouteRegistryStartsEmpty(t *testing.T) {
	reg := NewRouteRegistry()
	declared := reg.Declared()
	if len(declared) != 0 {
		t.Fatalf("new registry should be empty, got %d entries", len(declared))
	}
}

func TestRouteRegistryRecordAndDeclared(t *testing.T) {
	reg := NewRouteRegistry()
	reg.record("GET", "/workspaces/:workspaceID", PermWorkspaceView)
	reg.record("POST", "/workspaces/:workspaceID/deletion", PermWorkspaceDelete)

	declared := reg.Declared()
	if len(declared) != 2 {
		t.Fatalf("expected 2 declarations, got %d", len(declared))
	}

	key1 := routeKey{Method: "GET", Path: "/workspaces/:workspaceID"}
	if p, ok := declared[key1]; !ok || p != PermWorkspaceView {
		t.Errorf("expected %s for %v, got %s (ok=%v)", PermWorkspaceView, key1, p, ok)
	}

	key2 := routeKey{Method: "POST", Path: "/workspaces/:workspaceID/deletion"}
	if p, ok := declared[key2]; !ok || p != PermWorkspaceDelete {
		t.Errorf("expected %s for %v, got %s (ok=%v)", PermWorkspaceDelete, key2, p, ok)
	}
}

func TestDeclaredReturnsCopy(t *testing.T) {
	reg := NewRouteRegistry()
	reg.record("GET", "/test", PermWorkspaceView)

	// Get a copy and mutate it.
	copy1 := reg.Declared()
	copy1[routeKey{Method: "DELETE", Path: "/injected"}] = PermWorkspaceDelete

	// The registry should be unchanged.
	declared := reg.Declared()
	if len(declared) != 1 {
		t.Fatalf("Declared() returned a reference instead of a copy; len = %d", len(declared))
	}
}

func TestScopedGroupRegistersOnEchoAndRecordsInRegistry(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("")

	sg := NewScopedGroup(parent, "/workspaces/:workspaceID", nil, reg)

	handler := func(c echo.Context) error { return c.String(http.StatusOK, "ok") }

	sg.GET("", PermWorkspaceView, handler)
	sg.POST("/deletion", PermWorkspaceDelete, handler)
	sg.PATCH("", PermWorkspaceRename, handler)
	sg.DELETE("/deletion", PermWorkspaceDelete, handler)

	// Verify the registry has all four declarations.
	declared := reg.Declared()
	if len(declared) != 4 {
		t.Fatalf("expected 4 declarations, got %d", len(declared))
	}

	expectations := map[routeKey]Permission{
		{Method: "GET", Path: "/workspaces/:workspaceID"}:             PermWorkspaceView,
		{Method: "POST", Path: "/workspaces/:workspaceID/deletion"}:   PermWorkspaceDelete,
		{Method: "PATCH", Path: "/workspaces/:workspaceID"}:           PermWorkspaceRename,
		{Method: "DELETE", Path: "/workspaces/:workspaceID/deletion"}: PermWorkspaceDelete,
	}
	for key, wantPerm := range expectations {
		gotPerm, ok := declared[key]
		if !ok {
			t.Errorf("expected declaration for %v, not found", key)
			continue
		}
		if gotPerm != wantPerm {
			t.Errorf("for %v: got %s, want %s", key, gotPerm, wantPerm)
		}
	}
}

// withSessionContext returns middleware that stores an authorized
// SessionContext before the RequirePermission middleware ScopedGroup now
// installs automatically runs.
func withSessionContext(sc SessionContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			SetSessionContext(c, sc)
			return next(c)
		}
	}
}

func TestScopedGroupHandlerIsInvoked(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("/api")
	parent.Use(withSessionContext(SessionContext{
		MembershipID: "m1",
		Permissions:  permissionMatrix[RoleOwner],
	}))

	sg := NewScopedGroup(parent, "/test", nil, reg)

	called := false
	sg.GET("/hello", PermWorkspaceView, func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "world")
	})

	// Fire a request through Echo to verify the handler is reachable.
	req := httptest.NewRequest(http.MethodGet, "/api/test/hello", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if !called {
		t.Error("handler was not invoked")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestScopedGroupWithMiddleware(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("")
	parent.Use(withSessionContext(SessionContext{
		MembershipID: "m1",
		Permissions:  permissionMatrix[RoleOwner],
	}))

	sg := NewScopedGroup(parent, "/ws", nil, reg)

	mwCalled := false
	mw := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			mwCalled = true
			return next(c)
		}
	}

	sg.POST("/action", PermVacancyCreate, func(c echo.Context) error {
		return c.String(http.StatusCreated, "created")
	}, mw)

	req := httptest.NewRequest(http.MethodPost, "/ws/action", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if !mwCalled {
		t.Error("middleware was not called")
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}
}

func TestScopedGroupDeniesRequestWithoutSessionContext(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("/api")

	sg := NewScopedGroup(parent, "/test", nil, reg)

	called := false
	sg.GET("/hello", PermWorkspaceView, func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "world")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test/hello", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if called {
		t.Error("handler must not run when RequirePermission finds no session context")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestScopedGroupDeniesRequestMissingPermission(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("/api")
	parent.Use(withSessionContext(SessionContext{
		MembershipID: "m1",
		Permissions:  permissionMatrix[RoleFinance],
	}))

	sg := NewScopedGroup(parent, "/test", nil, reg)

	called := false
	sg.POST("/delete-workspace", PermWorkspaceDelete, func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "deleted")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/test/delete-workspace", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if called {
		t.Error("handler must not run when the session lacks the required permission")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", rec.Code)
	}
}

func TestWorkspaceScopedRoutesIsNonEmpty(t *testing.T) {
	if len(workspaceScopedRoutes) == 0 {
		t.Fatal("workspaceScopedRoutes should not be empty")
	}
}

func TestWorkspaceScopedRoutesContainsExpectedRoutes(t *testing.T) {
	// Spot-check a selection of routes from the design's route surface table.
	expected := []routeKey{
		{Method: "GET", Path: "/workspaces/:workspaceID"},
		{Method: "PATCH", Path: "/workspaces/:workspaceID"},
		{Method: "POST", Path: "/workspaces/:workspaceID/deletion"},
		{Method: "DELETE", Path: "/workspaces/:workspaceID/deletion"},
		{Method: "GET", Path: "/workspaces/:workspaceID/members"},
		{Method: "POST", Path: "/workspaces/:workspaceID/ownership"},
		{Method: "GET", Path: "/workspaces/:workspaceID/invitations"},
		{Method: "GET", Path: "/workspaces/:workspaceID/audit-log"},
		{Method: "GET", Path: "/job-vacancies"},
		{Method: "POST", Path: "/job-vacancies"},
		{Method: "GET", Path: "/job-applications"},
		{Method: "GET", Path: "/employer/dashboard"},
		{Method: "GET", Path: "/companies"},
		{Method: "GET", Path: "/subscriptions/mine"},
		{Method: "GET", Path: "/invoices"},
		{Method: "GET", Path: "/payments"},
		{Method: "POST", Path: "/payment-methods"},
	}
	for _, key := range expected {
		if _, ok := workspaceScopedRoutes[key]; !ok {
			t.Errorf("workspaceScopedRoutes missing expected route %s %s", key.Method, key.Path)
		}
	}
}

func TestVerifyPassesWhenAllRoutesRegistered(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("")

	handler := func(c echo.Context) error { return c.String(http.StatusOK, "ok") }

	// Register every route in workspaceScopedRoutes through ScopedGroups.
	// We need to create appropriate ScopedGroups for different prefixes.
	wsSG := NewScopedGroup(parent, "/workspaces/:workspaceID", nil, reg)
	wsSG.GET("", PermWorkspaceView, handler)
	wsSG.PATCH("", PermWorkspaceRename, handler)
	wsSG.POST("/deletion", PermWorkspaceDelete, handler)
	wsSG.DELETE("/deletion", PermWorkspaceDelete, handler)
	wsSG.GET("/permissions", PermWorkspaceView, handler)
	wsSG.POST("/import-personal-data", PermWorkspaceImportPersonalData, handler)
	wsSG.GET("/members", PermMemberView, handler)
	wsSG.PATCH("/members/:membershipID", PermMemberRoleChange, handler)
	wsSG.DELETE("/members/:membershipID", PermMemberRemove, handler)
	wsSG.POST("/members/:membershipID/suspension", PermMemberRemove, handler)
	wsSG.DELETE("/members/:membershipID/suspension", PermMemberRemove, handler)
	wsSG.POST("/ownership", PermOwnershipTransfer, handler)
	wsSG.GET("/invitations", PermMemberInvite, handler)
	wsSG.POST("/invitations", PermMemberInvite, handler)
	wsSG.POST("/invitations/:id/resend", PermMemberInvite, handler)
	wsSG.DELETE("/invitations/:id", PermMemberInvite, handler)
	wsSG.GET("/audit-log", PermAuditView, handler)

	vacSG := NewScopedGroup(parent, "/job-vacancies", nil, reg)
	vacSG.GET("", PermVacancyView, handler)
	vacSG.POST("", PermVacancyCreate, handler)
	vacSG.POST("/update", PermVacancyUpdate, handler)
	vacSG.POST("/delete", PermVacancyClose, handler)

	// /job-vacancy singular
	vacSingleSG := NewScopedGroup(parent, "/job-vacancy", nil, reg)
	vacSingleSG.GET("", PermVacancyView, handler)

	appSG := NewScopedGroup(parent, "/job-applications", nil, reg)
	appSG.GET("", PermApplicationView, handler)
	appSG.GET("/:id/review", PermApplicationView, handler)
	appSG.POST("/decision", PermApplicationDecide, handler)

	empSG := NewScopedGroup(parent, "/employer", nil, reg)
	empSG.GET("/dashboard", PermWorkspaceView, handler)
	empSG.GET("/artisans", PermArtisanBrowse, handler)

	coSG := NewScopedGroup(parent, "/companies", nil, reg)
	coSG.GET("", PermCompanyCreate, handler)
	coSG.POST("", PermCompanyCreate, handler)
	coSG.POST("/update", PermCompanyUpdate, handler)
	coSG.POST("/delete", PermCompanyUpdate, handler)

	subSG := NewScopedGroup(parent, "/subscriptions", nil, reg)
	subSG.GET("/mine", PermSubscriptionManage, handler)
	subSG.POST("/purchase", PermSubscriptionManage, handler)
	subSG.POST("/cancel", PermSubscriptionManage, handler)

	invSG := NewScopedGroup(parent, "/invoices", nil, reg)
	invSG.GET("", PermInvoiceView, handler)
	invSG.POST("/:id/pay", PermInvoicePay, handler)

	paySG := NewScopedGroup(parent, "/payments", nil, reg)
	paySG.GET("", PermPayoutView, handler)

	pmSG := NewScopedGroup(parent, "/payment-methods", nil, reg)
	pmSG.POST("", PermPaymentMethodManage, handler)

	if err := reg.Verify(); err != nil {
		t.Fatalf("Verify should pass when all routes are registered: %v", err)
	}
}

func TestVerifyFailsWhenWorkspaceScopedRouteMissing(t *testing.T) {
	// Empty registry: no routes registered, but workspaceScopedRoutes is non-empty.
	reg := NewRouteRegistry()

	err := reg.Verify()
	if err == nil {
		t.Fatal("Verify should fail when workspaceScopedRoutes entries are missing from the registry")
	}
	if !strings.Contains(err.Error(), "was not registered through a ScopedGroup") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestVerifyFailsWhenExtraRouteRegistered(t *testing.T) {
	reg := NewRouteRegistry()
	// Register a route that is not in workspaceScopedRoutes.
	reg.record("GET", "/unknown/route", PermWorkspaceView)

	err := reg.Verify()
	if err == nil {
		t.Fatal("Verify should fail when a ScopedGroup route is not in workspaceScopedRoutes")
	}
	if !strings.Contains(err.Error(), "not in workspaceScopedRoutes") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestVerifyFailsForUnknownPermission(t *testing.T) {
	reg := NewRouteRegistry()
	// Register a route in workspaceScopedRoutes but with an invalid permission.
	reg.record("GET", "/workspaces/:workspaceID", Permission("bogus.permission"))

	err := reg.Verify()
	if err == nil {
		t.Fatal("Verify should fail when a declared permission is unknown")
	}
	if !strings.Contains(err.Error(), "unknown permission") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestVerifyExcludesSessionScopedRoutes(t *testing.T) {
	reg := NewRouteRegistry()
	// Register session-scoped routes through the registry (as if through a ScopedGroup).
	// These should not trigger the "not in workspaceScopedRoutes" error.
	reg.record("GET", "/workspaces", PermWorkspaceView)
	reg.record("POST", "/workspaces", PermWorkspaceView)
	reg.record("POST", "/workspaces/:workspaceID/switch", PermWorkspaceView)

	err := reg.Verify()
	if err == nil {
		// It will still fail because workspaceScopedRoutes entries are not registered,
		// but it should NOT fail because of the session-scoped routes.
		t.Skip("Expected failure from missing workspaceScopedRoutes entries, not from session-scoped routes")
	}
	// Verify the error does NOT mention the session-scoped routes.
	if strings.Contains(err.Error(), "/workspaces was registered through a ScopedGroup but is not") {
		t.Errorf("session-scoped route /workspaces should be excluded from check 4")
	}
}

func TestSessionScopedRoutesContainsExpectedEntries(t *testing.T) {
	expected := []routeKey{
		{Method: "GET", Path: "/workspaces"},
		{Method: "POST", Path: "/workspaces"},
		{Method: "POST", Path: "/workspaces/:workspaceID/switch"},
	}
	if len(sessionScopedRoutes) != len(expected) {
		t.Fatalf("sessionScopedRoutes has %d entries, want %d", len(sessionScopedRoutes), len(expected))
	}
	for _, key := range expected {
		if _, ok := sessionScopedRoutes[key]; !ok {
			t.Errorf("sessionScopedRoutes missing %s %s", key.Method, key.Path)
		}
	}
}
