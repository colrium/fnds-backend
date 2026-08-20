package workspaces

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/audit"
	"backend/configs"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// useWiringTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup. Mirrors useResolverTestMongo /
// usePostAuthTestMongo.
func useWiringTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// newWiringTestApp wires a single ScopedGroup route (nil resolver) behind
// withSessionContext (defined in guard_test.go), mirroring the pattern
// TestScopedGroupHandlerIsInvoked already uses to exercise handlers without
// the full credential chain.
func newWiringTestApp(sc SessionContext, method, path string, perm Permission, h echo.HandlerFunc) *echo.Echo {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("/api/v1")
	parent.Use(withSessionContext(sc))

	sg := NewScopedGroup(parent, "/workspaces/:workspaceID", nil, reg)
	switch method {
	case http.MethodGet:
		sg.GET(path, perm, h)
	case http.MethodPost:
		sg.POST(path, perm, h)
	case http.MethodPatch:
		sg.PATCH(path, perm, h)
	case http.MethodDelete:
		sg.DELETE(path, perm, h)
	}
	return e
}

func ownerSession(workspaceID, userID, membershipID string) SessionContext {
	return SessionContext{
		UserID:           userID,
		WorkspaceID:      workspaceID,
		MembershipID:     membershipID,
		MembershipStatus: MembershipActive,
		Role:             RoleOwner,
		Permissions:      permissionMatrix[RoleOwner],
	}
}

func decodeJSON(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, string(body))
	}
}

func TestHandleGetWorkspacePermissions_ReturnsRoleAndPermissions(t *testing.T) {
	sc := ownerSession("ws-1", "user-1", "m-1")
	app := newWiringTestApp(sc, http.MethodGet, "/permissions", PermWorkspaceView, handleGetWorkspacePermissions)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1/permissions", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data workspacePermissionsResponse `json:"data"`
	}
	decodeJSON(t, rec.Body.Bytes(), &resp)
	if resp.Data.Role != RoleOwner {
		t.Errorf("role = %q, want %q", resp.Data.Role, RoleOwner)
	}
	if len(resp.Data.Permissions) != len(AllPermissions) {
		t.Errorf("owner permissions = %d, want %d (all permissions)", len(resp.Data.Permissions), len(AllPermissions))
	}
}

func TestHandleGetWorkspacePermissions_Unauthenticated(t *testing.T) {
	e := echo.New()
	reg := NewRouteRegistry()
	parent := e.Group("/api/v1")
	sg := NewScopedGroup(parent, "/workspaces/:workspaceID", nil, reg)
	sg.GET("/permissions", PermWorkspaceView, handleGetWorkspacePermissions)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1/permissions", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandleGetWorkspace_Success(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("returns the workspace document", func(mt *mtest.T) {
		useWiringTestMongo(mt)
		ns := mt.DB.Name() + ".workspaces"

		now := time.Now().UTC().Truncate(time.Second)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns, mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "ws-1"},
			{Key: "name", Value: "Acme Co"},
			{Key: "name_normalized", Value: "acme co"},
			{Key: "slug", Value: "acme-co"},
			{Key: "created_by", Value: "user-1"},
			{Key: "created_at", Value: now},
			{Key: "updated_at", Value: now},
		}))

		cfg := RouteConfig{WorkspaceService: &WorkspaceService{}}
		sc := ownerSession("ws-1", "user-1", "m-1")
		app := newWiringTestApp(sc, http.MethodGet, "", PermWorkspaceView, handleGetWorkspace(cfg))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data Workspace `json:"data"`
		}
		decodeJSON(t, rec.Body.Bytes(), &resp)
		if resp.Data.ID != "ws-1" || resp.Data.Name != "Acme Co" {
			mt.Errorf("unexpected workspace in response: %+v", resp.Data)
		}
	})
}

func TestHandleGetWorkspace_NotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("returns 404 workspace_not_found", func(mt *mtest.T) {
		useWiringTestMongo(mt)
		ns := mt.DB.Name() + ".workspaces"
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns, mtest.FirstBatch))

		cfg := RouteConfig{WorkspaceService: &WorkspaceService{}}
		sc := ownerSession("ws-1", "user-1", "m-1")
		app := newWiringTestApp(sc, http.MethodGet, "", PermWorkspaceView, handleGetWorkspace(cfg))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			mt.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "workspace_not_found") {
			mt.Errorf("expected workspace_not_found code in body, got %s", rec.Body.String())
		}
	})
}

func TestHandleRenameWorkspace_InvalidBody(t *testing.T) {
	cfg := RouteConfig{WorkspaceService: &WorkspaceService{}}
	sc := ownerSession("ws-1", "user-1", "m-1")
	app := newWiringTestApp(sc, http.MethodPatch, "", PermWorkspaceRename, handleRenameWorkspace(cfg))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/ws-1", strings.NewReader("not-json"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRenameWorkspace_InvalidName(t *testing.T) {
	cfg := RouteConfig{WorkspaceService: &WorkspaceService{}}
	sc := ownerSession("ws-1", "user-1", "m-1")
	app := newWiringTestApp(sc, http.MethodPatch, "", PermWorkspaceRename, handleRenameWorkspace(cfg))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/ws-1", strings.NewReader(`{"name":"a"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	// ValidateName rejects a name under 2 characters before any Mongo call is made.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_workspace_name") {
		t.Errorf("expected invalid_workspace_name code in body, got %s", rec.Body.String())
	}
}

func TestHandleScheduleDeletion_InvalidBody(t *testing.T) {
	cfg := RouteConfig{WorkspaceService: &WorkspaceService{}}
	sc := ownerSession("ws-1", "user-1", "m-1")
	app := newWiringTestApp(sc, http.MethodPost, "/deletion", PermWorkspaceDelete, handleScheduleDeletion(cfg))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws-1/deletion", strings.NewReader("not-json"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCancelDeletion_Success(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("cancels deletion and writes an audit entry", func(mt *mtest.T) {
		useWiringTestMongo(mt)

		mt.AddMockResponses(
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),
			mtest.CreateSuccessResponse(),
		)

		cfg := RouteConfig{
			WorkspaceService: &WorkspaceService{audit: audit.NewLogger(), clock: fixedTestClock{}},
		}
		sc := ownerSession("ws-1", "user-1", "m-1")
		app := newWiringTestApp(sc, http.MethodDelete, "/deletion", PermWorkspaceDelete, handleCancelDeletion(cfg))

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/ws-1/deletion", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleListMembers_Success(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("returns an empty member list when the workspace has none", func(mt *mtest.T) {
		useWiringTestMongo(mt)
		ns := mt.DB.Name() + ".memberships"
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns, mtest.FirstBatch))

		cfg := RouteConfig{MembershipService: &MembershipService{}}
		sc := ownerSession("ws-1", "user-1", "m-1")
		app := newWiringTestApp(sc, http.MethodGet, "/members", PermMemberView, handleListMembers(cfg))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws-1/members", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data []MemberView `json:"data"`
		}
		decodeJSON(t, rec.Body.Bytes(), &resp)
		if len(resp.Data) != 0 {
			mt.Errorf("expected 0 members, got %d", len(resp.Data))
		}
	})
}

func TestHandleChangeMemberRole_InvalidBody(t *testing.T) {
	cfg := RouteConfig{MembershipService: &MembershipService{}}
	sc := ownerSession("ws-1", "user-1", "m-1")
	app := newWiringTestApp(sc, http.MethodPatch, "/members/:membershipID", PermMemberRoleChange, handleChangeMemberRole(cfg))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/ws-1/members/m-2", strings.NewReader("not-json"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleTransferOwnership_InvalidBody(t *testing.T) {
	cfg := RouteConfig{MembershipService: &MembershipService{}}
	sc := ownerSession("ws-1", "user-1", "m-1")
	app := newWiringTestApp(sc, http.MethodPost, "/ownership", PermOwnershipTransfer, handleTransferOwnership(cfg))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws-1/ownership", strings.NewReader("not-json"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleSetMemberStatus_MembershipNotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("404s when the target membership does not exist", func(mt *mtest.T) {
		useWiringTestMongo(mt)
		ns := mt.DB.Name() + ".memberships"
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns, mtest.FirstBatch))

		cfg := RouteConfig{MembershipService: &MembershipService{}}
		sc := ownerSession("ws-1", "user-1", "m-1")
		h := handleSetMemberStatus(cfg, MembershipRemoved, "Member removed")
		app := newWiringTestApp(sc, http.MethodDelete, "/members/:membershipID", PermMemberRemove, h)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/ws-1/members/m-ghost", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			mt.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "membership_not_found") {
			mt.Errorf("expected membership_not_found code, got %s", rec.Body.String())
		}
	})
}

func TestHandleImportPersonalData_Success(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("re-scopes unscoped companies, vacancies, and subscriptions", func(mt *mtest.T) {
		useWiringTestMongo(mt)

		mt.AddMockResponses(
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 2}, bson.E{Key: "nModified", Value: 2}),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 0}, bson.E{Key: "nModified", Value: 0}),
		)

		cfg := RouteConfig{WorkspaceService: &WorkspaceService{clock: fixedTestClock{}}}
		sc := ownerSession("ws-1", "user-1", "m-1")
		app := newWiringTestApp(sc, http.MethodPost, "/import-personal-data", PermWorkspaceImportPersonalData, handleImportPersonalData(cfg))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/ws-1/import-personal-data", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data RescopeReport `json:"data"`
		}
		decodeJSON(t, rec.Body.Bytes(), &resp)
		if resp.Data.Companies != 2 || resp.Data.Vacancies != 1 || resp.Data.Subscriptions != 0 {
			mt.Errorf("unexpected report: %+v", resp.Data)
		}
	})
}

// fixedTestClock is a minimal stytch.Clock implementation for tests that only
// need a stable, non-zero time (e.g. for the updated_at set by CancelDeletion).
type fixedTestClock struct{}

func (fixedTestClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
