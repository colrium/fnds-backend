package invitations

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/ratelimit"
	"backend/stytch"
	"backend/workspaces"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// useHandlersTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup, mirroring workspaces' useResolverTestMongo.
func useHandlersTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// fixedClock is a minimal stytch.Clock for deterministic test timestamps.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func newTestHandlersConfig() HandlersConfig {
	clock := fixedClock{now: time.Now()}
	rate := ratelimit.NewLimiter(nil, nil)
	auditLogger := audit.NewLogger()
	fake := stytch.NewFake(clock)
	adapter := stytch.NewWithClient(stytch.Config{
		ProjectID:     "project-test",
		Secret:        "secret-test",
		PublicToken:   "pub-test",
		Environment:   "test",
		WebhookSecret: "whsec_test",
		Timeout:       5 * time.Second,
		JWKSTTL:       time.Hour,
		MaxAttempts:   3,
		Clock:         clock,
	}, fake)

	membershipService := workspaces.NewMembershipService(adapter, auditLogger, clock, false)
	svc := NewService(rate, auditLogger, clock, membershipService)
	workspaceService := workspaces.NewWorkspaceService(adapter, auditLogger, clock, rate)

	return HandlersConfig{
		Service:          svc,
		Adapter:          adapter,
		WorkspaceService: workspaceService,
	}
}

// newInvitationEchoContext builds an echo.Context for a request carrying the
// given :token path param and JSON body.
func newInvitationEchoContext(method, path, token, body string) (echo.Context, *httptest.ResponseRecorder) {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if token != "" {
		c.SetParamNames("token")
		c.SetParamValues(token)
	}
	return c, rec
}

// withSessionContext attaches a resolved SessionContext to c, as
// workspaces.Resolver.RequireSession would for an authenticated
// workspace-scoped request.
func withSessionContext(c echo.Context, workspaceID, userID string) {
	workspaces.SetSessionContext(c, workspaces.SessionContext{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Role:        workspaces.RoleOwner,
	})
}

// --- Resolve ---------------------------------------------------------------

func TestResolveHandler_NotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no invitation matches the token", func(mt *mtest.T) {
		useHandlersTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch))

		h := resolveHandler(newTestHandlersConfig())
		c, rec := newInvitationEchoContext(http.MethodGet, "/invitations/bad-token", "bad-token", "")

		if err := h(c); err != nil {
			mt.Fatalf("handler returned error: %v", err)
		}
		if rec.Code != http.StatusNotFound {
			mt.Errorf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "invitation_not_found") {
			mt.Errorf("body missing invitation_not_found code: %s", rec.Body.String())
		}
	})
}

// --- Decline ----------------------------------------------------------------

func TestDeclineHandler_NotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no invitation matches the token", func(mt *mtest.T) {
		useHandlersTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		// findInvitationByToken (for the notify-inviter lookup) then Decline's
		// own lookup both query token_hash — both come back empty.
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch),
			mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch),
		)

		h := declineHandler(newTestHandlersConfig())
		c, rec := newInvitationEchoContext(http.MethodPost, "/invitations/bad-token/decline", "bad-token", "")

		if err := h(c); err != nil {
			mt.Fatalf("handler returned error: %v", err)
		}
		if rec.Code != http.StatusNotFound {
			mt.Errorf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})
}

// --- Accept ------------------------------------------------------------------

func TestAcceptHandler_MissingDiscoveryToken(t *testing.T) {
	cfg := newTestHandlersConfig()
	h := acceptHandler(cfg)

	c, rec := newInvitationEchoContext(http.MethodPost, "/invitations/some-token/accept", "some-token", `{}`)

	if err := h(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "token_required") {
		t.Errorf("body missing token_required code: %s", rec.Body.String())
	}
}

func TestAcceptHandler_InvitationNotFound(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no invitation matches the token", func(mt *mtest.T) {
		useHandlersTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch))

		h := acceptHandler(newTestHandlersConfig())
		body := `{"discovery_token":"tok-123","type":"magic_link"}`
		c, rec := newInvitationEchoContext(http.MethodPost, "/invitations/bad-token/accept", "bad-token", body)

		if err := h(c); err != nil {
			mt.Fatalf("handler returned error: %v", err)
		}
		if rec.Code != http.StatusNotFound {
			mt.Errorf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
	})
}

// --- List (workspace-scoped) ------------------------------------------------

func TestListHandler_RequiresSession(t *testing.T) {
	h := listHandler(newTestHandlersConfig())
	c, rec := newInvitationEchoContext(http.MethodGet, "/workspaces/ws-1/invitations", "", "")

	if err := h(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestListHandler_Success(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("delegates to Service.List using the session's workspace", func(mt *mtest.T) {
		useHandlersTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns("invitations"), mtest.FirstBatch))

		h := listHandler(newTestHandlersConfig())
		c, rec := newInvitationEchoContext(http.MethodGet, "/workspaces/ws-1/invitations", "", "")
		withSessionContext(c, "ws-1", "user-1")

		if err := h(c); err != nil {
			mt.Fatalf("handler returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			mt.Errorf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"invitations":[]`) {
			mt.Errorf("body missing empty invitations array: %s", rec.Body.String())
		}
	})
}

// --- Create (workspace-scoped) ----------------------------------------------

func TestCreateHandler_RequiresSession(t *testing.T) {
	h := createHandler(newTestHandlersConfig())
	c, rec := newInvitationEchoContext(http.MethodPost, "/workspaces/ws-1/invitations", "", `{"email":"a@b.com","role":"admin"}`)

	if err := h(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- Resend (workspace-scoped) -----------------------------------------------

func TestResendHandler_RequiresSession(t *testing.T) {
	h := resendHandler(newTestHandlersConfig())
	c, rec := newInvitationEchoContext(http.MethodPost, "/workspaces/ws-1/invitations/inv-1/resend", "", "")
	c.SetParamNames("id")
	c.SetParamValues("inv-1")

	if err := h(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- Revoke (workspace-scoped) -----------------------------------------------

func TestRevokeHandler_RequiresSession(t *testing.T) {
	h := revokeHandler(newTestHandlersConfig())
	c, rec := newInvitationEchoContext(http.MethodDelete, "/workspaces/ws-1/invitations/inv-1", "", "")
	c.SetParamNames("id")
	c.SetParamValues("inv-1")

	if err := h(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- respondInvitationError --------------------------------------------------

func TestRespondInvitationError_MapsInvitationError(t *testing.T) {
	c, rec := newInvitationEchoContext(http.MethodGet, "/invitations/x", "x", "")

	err := respondInvitationError(c, &InvitationError{
		Status:  http.StatusConflict,
		Code:    "invitation_not_pending",
		Message: "cannot accept an invitation with status \"accepted\"",
	})
	if err != nil {
		t.Fatalf("respondInvitationError returned error: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "invitation_not_pending") {
		t.Errorf("body missing mapped code: %s", rec.Body.String())
	}
}

func TestRespondInvitationError_UnexpectedErrorMapsTo500(t *testing.T) {
	c, rec := newInvitationEchoContext(http.MethodGet, "/invitations/x", "x", "")

	err := respondInvitationError(c, bson.ErrDecodeToNil)
	if err != nil {
		t.Fatalf("respondInvitationError returned error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
