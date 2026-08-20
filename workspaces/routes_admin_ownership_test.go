package workspaces

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/audit"
	"backend/contextutil"
	"backend/middleware"
	"backend/stytch"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// newAdminOwnershipTestApp wires POST /admin/workspaces/:workspaceID/ownership
// exactly as RegisterRoutes does — behind middleware.IsAdmin and outside the
// workspace-scoped ScopedGroup registry. asUserID stands in for JWTAuth, which
// is all IsAdmin reads (contextutil.GetUserID).
func newAdminOwnershipTestApp(asUserID string) *echo.Echo {
	clock := stytch.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := RouteConfig{
		MembershipService: NewMembershipService(nil, audit.NewLogger(), clock, true),
		Clock:             clock,
	}

	app := echo.New()
	group := app.Group("/api/v1/admin")
	group.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			contextutil.SetUser(c, asUserID)
			return next(c)
		}
	}, middleware.IsAdmin)
	group.POST("/workspaces/:workspaceID/ownership", handleAdminTransferOwnership(cfg))
	return app
}

func adminOwnershipRequest(app *echo.Echo, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/workspaces/ws-1/ownership",
		bytes.NewBufferString(body),
	)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

// userLookupResponse mocks the users.GetUserByID FindOne that IsAdmin performs.
func userLookupResponse(userID, userType string) bson.D {
	return mtest.CreateCursorResponse(0, "fundis.users", mtest.FirstBatch, bson.D{
		{Key: "_id", Value: userID},
		{Key: "user_type", Value: userType},
		{Key: "status", Value: "active"},
	})
}

func TestAdminTransferOwnershipRouteRejectsNonAdminCaller(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("customer caller", func(mt *mtest.T) {
		useRetentionTestMongo(mt)
		mt.AddMockResponses(userLookupResponse("user-1", "customer"))

		recorder := adminOwnershipRequest(
			newAdminOwnershipTestApp("user-1"),
			`{"target_membership_id":"m-2"}`,
		)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestAdminTransferOwnershipRouteRejectsUnauthenticatedCaller(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no user", func(mt *mtest.T) {
		useRetentionTestMongo(mt)

		recorder := adminOwnershipRequest(
			newAdminOwnershipTestApp(""),
			`{"target_membership_id":"m-2"}`,
		)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestAdminTransferOwnershipRouteRequiresTargetMembershipID(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("missing target", func(mt *mtest.T) {
		useRetentionTestMongo(mt)
		mt.AddMockResponses(userLookupResponse("admin-1", "admin"))

		recorder := adminOwnershipRequest(newAdminOwnershipTestApp("admin-1"), `{}`)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

// An admin caller reaches MembershipService.AdminTransferOwnership: with no
// active owner membership in the workspace the service's own 404 surfaces,
// which proves the route calls the service rather than returning a canned
// success.
func TestAdminTransferOwnershipRouteReachesTheServiceForAnAdminCaller(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("admin caller", func(mt *mtest.T) {
		useRetentionTestMongo(mt)
		mt.AddMockResponses(
			userLookupResponse("admin-1", "admin"),
			// memberships FindOne for the current active owner: none.
			mtest.CreateCursorResponse(0, "fundis.memberships", mtest.FirstBatch),
		)

		recorder := adminOwnershipRequest(
			newAdminOwnershipTestApp("admin-1"),
			`{"target_membership_id":"m-2"}`,
		)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
		}
		if !bytes.Contains(recorder.Body.Bytes(), []byte("no_active_owner")) {
			t.Fatalf("body does not carry the service error code: %s", recorder.Body.String())
		}
	})
}
