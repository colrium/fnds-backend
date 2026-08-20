package workspaces

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/stytch"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// useRoutesTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup. Mirrors useResolverTestMongo /
// usePostAuthTestMongo.
func useRoutesTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// newTestRouteConfig builds a RouteConfig backed by a fake Stytch adapter and
// a real WorkspaceService, sufficient to exercise the three session-scoped
// handlers without a live Stytch project or database.
func newTestRouteConfig(t testing.TB) (RouteConfig, *stytch.Fake, *testClock) {
	t.Helper()
	clock := newTestClock()
	fake := stytch.NewFake(clock)
	cfg := stytch.Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}
	adapter := stytch.NewWithClient(cfg, fake)
	svc := NewWorkspaceService(adapter, audit.NewLogger(), clock, nil)
	return RouteConfig{Adapter: adapter, WorkspaceService: svc}, fake, clock
}

func newEchoContextForRoutes(method, path, body, bearer string) (echo.Context, *httptest.ResponseRecorder) {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	return c, rec
}

// --- GET /workspaces ---

func TestHandleListWorkspaces_EmptyList(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("zero memberships returns 200 with an empty list, not an error", func(mt *mtest.T) {
		useRoutesTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch),
		)

		cfg, _, _ := newTestRouteConfig(mt)
		c, rec := newEchoContextForRoutes(http.MethodGet, "/api/v1/workspaces", "", "token")
		SetSessionContext(c, SessionContext{UserID: "user-1"})

		if err := handleListWorkspaces(cfg)(c); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		var payload struct {
			Data []WorkspaceSummary `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			mt.Fatalf("decode response: %v", err)
		}
		if len(payload.Data) != 0 {
			mt.Errorf("data length = %d, want 0", len(payload.Data))
		}
	})
}

func TestHandleListWorkspaces_Populated(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("returns every workspace the user belongs to", func(mt *mtest.T) {
		useRoutesTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "m-1"},
				{Key: "workspace_id", Value: "ws-1"},
				{Key: "user_id", Value: "user-1"},
				{Key: "role", Value: "owner"},
				{Key: "status", Value: "active"},
				{Key: "authority_version", Value: int64(1)},
				{Key: "last_active_at", Value: time.Now()},
				{Key: "joined_at", Value: time.Now()},
				{Key: "created_at", Value: time.Now()},
				{Key: "updated_at", Value: time.Now()},
			}),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "ws-1"},
				{Key: "name", Value: "Acme"},
				{Key: "slug", Value: "acme"},
				{Key: "created_at", Value: time.Now()},
			}),
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "ws-1"},
				{Key: "count", Value: int32(1)},
			}),
		)

		cfg, _, _ := newTestRouteConfig(mt)
		c, rec := newEchoContextForRoutes(http.MethodGet, "/api/v1/workspaces", "", "token")
		SetSessionContext(c, SessionContext{UserID: "user-1"})

		if err := handleListWorkspaces(cfg)(c); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		var payload struct {
			Data []WorkspaceSummary `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			mt.Fatalf("decode response: %v", err)
		}
		if len(payload.Data) != 1 || payload.Data[0].ID != "ws-1" {
			mt.Fatalf("data = %+v, want one summary for ws-1", payload.Data)
		}
	})
}

func TestHandleListWorkspaces_Unauthenticated(t *testing.T) {
	c, rec := newEchoContextForRoutes(http.MethodGet, "/api/v1/workspaces", "", "")
	cfg := RouteConfig{}

	if err := handleListWorkspaces(cfg)(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// --- POST /workspaces ---

func TestHandleCreateWorkspace_InvalidName(t *testing.T) {
	cfg := RouteConfig{}
	c, rec := newEchoContextForRoutes(http.MethodPost, "/api/v1/workspaces", `{"workspace_name":"a"}`, "token")
	SetSessionContext(c, SessionContext{UserID: "user-1"})

	if err := handleCreateWorkspace(cfg)(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateWorkspace_HappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("creates a Stytch organization, a Stytch member, and the Workspace/Membership documents", func(mt *mtest.T) {
		useRoutesTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			// users.FindOne — load the caller's email/name.
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "user-1"},
				{Key: "email", Value: "owner@example.com"},
				{Key: "first_name", Value: "Ada"},
				{Key: "last_name", Value: "Lovelace"},
			}),
			// memberships.CountDocuments — the one-workspace-per-employer cap.
			// Zero, so this caller is creating their first workspace and the cap
			// permits it.
			mtest.CreateCursorResponse(1, ns("memberships"), mtest.FirstBatch, bson.D{
				{Key: "n", Value: int32(0)},
			}),
			// workspaces.InsertOne (insertWorkspaceWithSlugRetry).
			mtest.CreateSuccessResponse(),
			// memberships.InsertOne (owner membership).
			mtest.CreateSuccessResponse(),
			// audit_log_entries.InsertOne.
			mtest.CreateSuccessResponse(),
		)

		cfg, _, _ := newTestRouteConfig(mt)
		c, rec := newEchoContextForRoutes(http.MethodPost, "/api/v1/workspaces", `{"workspace_name":"Second Company"}`, "session-token-1")
		SetSessionContext(c, SessionContext{UserID: "user-1"})

		if err := handleCreateWorkspace(cfg)(c); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			mt.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}

		var payload struct {
			Data createWorkspaceResponse `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			mt.Fatalf("decode response: %v", err)
		}
		if payload.Data.WorkspaceID == "" {
			mt.Error("expected a non-empty workspace_id")
		}
		if payload.Data.Name != "Second Company" {
			mt.Errorf("name = %q, want %q", payload.Data.Name, "Second Company")
		}
		if payload.Data.Role != RoleOwner {
			mt.Errorf("role = %q, want %q", payload.Data.Role, RoleOwner)
		}
	})
}

func TestHandleCreateWorkspace_Unauthenticated(t *testing.T) {
	cfg := RouteConfig{}
	c, rec := newEchoContextForRoutes(http.MethodPost, "/api/v1/workspaces", `{"workspace_name":"Second Company"}`, "")

	if err := handleCreateWorkspace(cfg)(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// --- POST /workspaces/:workspaceID/switch ---

func TestHandleSwitchWorkspace_HappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("switches to a workspace the user actively belongs to", func(mt *mtest.T) {
		useRoutesTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		cfg, fake, _ := newTestRouteConfig(mt)

		// Seed the fake with a source org (the caller's current session lives
		// here) and a target org (ws-2's Stytch organization) so
		// ExchangeSession can find both sides of the exchange.
		sourceOrg, err := fake.CreateOrganizationFromDiscovery(context.Background(), stytch.CreateOrgInput{
			IntermediateSessionToken: "intermediate-1", Name: "Source", Slug: "source",
		})
		if err != nil {
			mt.Fatalf("seed source org: %v", err)
		}
		targetOrg, err := fake.CreateOrganization(context.Background(), stytch.CreateOrganizationInput{Name: "Target", Slug: "target"})
		if err != nil {
			mt.Fatalf("seed target org: %v", err)
		}

		mt.AddMockResponses(
			// memberships.FindOne — active membership in the target workspace.
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "m-2"},
				{Key: "workspace_id", Value: "ws-2"},
				{Key: "user_id", Value: "user-1"},
				{Key: "role", Value: "hr"},
				{Key: "status", Value: "active"},
				{Key: "authority_version", Value: int64(1)},
				{Key: "last_active_at", Value: time.Now()},
				{Key: "joined_at", Value: time.Now()},
				{Key: "created_at", Value: time.Now()},
				{Key: "updated_at", Value: time.Now()},
			}),
			// workspaces.FindOne — resolve the target workspace's Stytch org ID.
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "ws-2"},
				{Key: "name", Value: "Workspace Two"},
				{Key: "slug", Value: "workspace-two"},
				{Key: "stytch_organization_id", Value: targetOrg.OrganizationID},
				{Key: "created_by", Value: "user-1"},
				{Key: "created_at", Value: time.Now()},
				{Key: "updated_at", Value: time.Now()},
			}),
			// memberships.UpdateOne — last_active_at / stytch_member_id refresh.
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),
		)

		c, rec := newEchoContextForRoutes(http.MethodPost, "/api/v1/workspaces/ws-2/switch", "", sourceOrg.SessionToken)
		c.SetParamNames("workspaceID")
		c.SetParamValues("ws-2")
		SetSessionContext(c, SessionContext{UserID: "user-1"})

		if err := handleSwitchWorkspace(cfg)(c); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		var payload struct {
			Data SwitchResult `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			mt.Fatalf("decode response: %v", err)
		}
		if payload.Data.WorkspaceID != "ws-2" {
			mt.Errorf("workspace_id = %q, want %q", payload.Data.WorkspaceID, "ws-2")
		}
		if payload.Data.Role != RoleHR {
			mt.Errorf("role = %q, want %q", payload.Data.Role, RoleHR)
		}
		if payload.Data.SessionJWT == "" {
			mt.Error("expected a non-empty session_jwt")
		}
	})
}

func TestHandleSwitchWorkspace_NoActiveMembership(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no active membership in the target workspace leaves it unchanged and returns 404", func(mt *mtest.T) {
		useRoutesTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch),
		)

		cfg, _, _ := newTestRouteConfig(mt)
		c, rec := newEchoContextForRoutes(http.MethodPost, "/api/v1/workspaces/ws-ghost/switch", "", "session-token-1")
		c.SetParamNames("workspaceID")
		c.SetParamValues("ws-ghost")
		SetSessionContext(c, SessionContext{UserID: "user-1"})

		if err := handleSwitchWorkspace(cfg)(c); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusNotFound {
			mt.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleSwitchWorkspace_MissingBearerToken(t *testing.T) {
	cfg := RouteConfig{}
	c, rec := newEchoContextForRoutes(http.MethodPost, "/api/v1/workspaces/ws-2/switch", "", "")
	c.SetParamNames("workspaceID")
	c.SetParamValues("ws-2")
	SetSessionContext(c, SessionContext{UserID: "user-1"})

	if err := handleSwitchWorkspace(cfg)(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}
