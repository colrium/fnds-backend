package adminapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/auth"
	"backend/jwtutil"
	"backend/middleware"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// buildAdminGroup wires the exact middleware chain main.go uses for the real
// /api/v1/admin group (JWTAuth then IsAdmin) in front of the adminapi routes,
// so tests exercise the actual authorization boundary instead of asserting
// route-table membership only.
func buildAdminGroup() *echo.Echo {
	e := echo.New()
	g := e.Group("/admin")
	g.Use(middleware.JWTAuth, middleware.IsAdmin)
	RegisterAdminRoutes(g)
	return e
}

func userLookupResponse(userID, userType string) bson.D {
	return bson.D{
		{Key: "_id", Value: userID},
		{Key: "user_type", Value: userType},
		{Key: "status", Value: "active"},
	}
}

// queueUserLookup queues the mock response for one call to users.GetUserByID.
// A single authenticated request through JWTAuth + a role guard looks the
// caller up twice (once per middleware), so callers queue this twice per
// request that is expected to reach the role check.
func queueUserLookup(mt *mtest.T, userID, userType string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch, userLookupResponse(userID, userType),
	))
}

func TestAdminGroupRejectsRequestWithNoToken(t *testing.T) {
	app := buildAdminGroup()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminGroupRejectsNonAdminJWT(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	queueUserLookup(mt, "artisan-1", "artisan") // JWTAuth's lookup
	queueUserLookup(mt, "artisan-1", "artisan") // IsAdmin's lookup

	token, _, err := jwtutil.GenerateToken("artisan-1")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	app := buildAdminGroup()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a non-admin JWT; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminGroupAllowsAdminJWT(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	queueUserLookup(mt, "admin-1", "admin") // JWTAuth's lookup
	queueUserLookup(mt, "admin-1", "admin") // IsAdmin's lookup
	// HandleListUsers itself then issues its own Find + CountDocuments.
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	token, _, err := jwtutil.GenerateToken("admin-1")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	app := buildAdminGroup()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an admin JWT; body: %s", rec.Code, rec.Body.String())
	}
}

// Regression test for the fixed bug (see auth/admin_routes_test.go for the
// route-registration half): POST /admin/users, reached through the real
// JWTAuth+IsAdmin chain, must reject a non-admin caller end-to-end.
func TestAdminUsersCreateEndpointRejectsNonAdminCallerEndToEnd(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	queueUserLookup(mt, "customer-1", "customer") // JWTAuth's lookup
	queueUserLookup(mt, "customer-1", "customer") // IsAdmin's lookup

	token, _, err := jwtutil.GenerateToken("customer-1")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	e := echo.New()
	g := e.Group("/admin")
	g.Use(middleware.JWTAuth, middleware.IsAdmin)
	auth.RegisterAdminRoutes(g)
	RegisterAdminRoutes(g)

	req := httptest.NewRequest(http.MethodPost, "/admin/users", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a non-admin JWT; body: %s", rec.Code, rec.Body.String())
	}
}
