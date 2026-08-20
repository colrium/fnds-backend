package workspaces

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/configs"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"backend/stytch"
)

// useResolverTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup. Mirrors usePostAuthTestMongo.
func useResolverTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

func newTestResolver(t testing.TB) (*Resolver, *stytch.Fake, *testClock) {
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
	return NewResolver(adapter, clock), fake, clock
}

func newEchoContextWithAuth(bearer string, workspaceID string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/workspaces/"+workspaceID, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	if workspaceID != "" {
		c.SetParamNames("workspaceID")
		c.SetParamValues(workspaceID)
	}
	return c, rec
}

func TestResolver_AuthenticateHeader_MissingHeader(t *testing.T) {
	r, _, _ := newTestResolver(t)
	c, _ := newEchoContextWithAuth("", "")

	_, err := r.authenticateHeader(c)
	if err == nil {
		t.Fatal("expected an error for a missing Authorization header")
	}
}

func TestResolver_AuthenticateHeader_MalformedHeader(t *testing.T) {
	r, _, _ := newTestResolver(t)
	req := httptest.NewRequest(http.MethodGet, "/workspaces/ws-1", nil)
	req.Header.Set("Authorization", "NotBearer sometoken")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	_, err := r.authenticateHeader(c)
	if err == nil {
		t.Fatal("expected an error for a non-Bearer Authorization header")
	}
}

func TestResolver_AuthenticateHeader_ValidStytchJWT(t *testing.T) {
	r, fake, _ := newTestResolver(t)
	token := fake.MintSessionJWT("member-1", "org-1", time.Hour)
	c, _ := newEchoContextWithAuth(token, "")

	id, err := r.authenticateHeader(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.kind != CredentialStytchJWT {
		t.Errorf("kind = %v, want CredentialStytchJWT", id.kind)
	}
	if id.memberID != "member-1" {
		t.Errorf("memberID = %q, want %q", id.memberID, "member-1")
	}
}

func membershipDoc(id, userID, workspaceID, stytchMemberID string, authorityVersion int64) bson.D {
	return bson.D{
		{Key: "_id", Value: id},
		{Key: "workspace_id", Value: workspaceID},
		{Key: "user_id", Value: userID},
		{Key: "role", Value: "owner"},
		{Key: "status", Value: "active"},
		{Key: "authority_version", Value: authorityVersion},
		{Key: "stytch_member_id", Value: stytchMemberID},
		{Key: "last_active_at", Value: time.Now()},
		{Key: "joined_at", Value: time.Now()},
		{Key: "created_at", Value: time.Now()},
		{Key: "updated_at", Value: time.Now()},
	}
}

func workspaceDoc(id string) bson.D {
	return bson.D{
		{Key: "_id", Value: id},
		{Key: "name", Value: "Test Workspace"},
		{Key: "name_normalized", Value: "test workspace"},
		{Key: "slug", Value: "test-workspace"},
		{Key: "stytch_organization_id", Value: "org-1"},
		{Key: "created_by", Value: "user-1"},
		{Key: "created_at", Value: time.Now()},
		{Key: "updated_at", Value: time.Now()},
	}
}

func TestResolver_Resolve_Success(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("valid Stytch credential with membership, account link, and workspace resolves", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, membershipDoc("m-1", "user-1", "ws-1", "member-1", 1)),
			mtest.CreateCursorResponse(0, ns("account_links"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "link-1"},
				{Key: "user_id", Value: "user-1"},
				{Key: "stytch_member_id", Value: "member-1"},
				{Key: "stytch_organization_id", Value: "org-1"},
				{Key: "linked_email", Value: "user@example.com"},
				{Key: "link_method", Value: "signup"},
				{Key: "created_at", Value: time.Now()},
				{Key: "updated_at", Value: time.Now()},
			}),
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "user-1"},
				{Key: "email", Value: "user@example.com"},
				{Key: "user_type", Value: "customer"},
				{Key: "status", Value: "active"},
			}),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
		)

		r := NewResolver(nil, newTestClock())
		id := resolvedIdentity{kind: CredentialStytchJWT, memberID: "member-1", sessionID: "session-1"}

		sc, err := r.Resolve(context.Background(), id, "ws-1")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
		if sc.UserID != "user-1" {
			mt.Errorf("UserID = %q, want %q", sc.UserID, "user-1")
		}
		if sc.WorkspaceID != "ws-1" {
			mt.Errorf("WorkspaceID = %q, want %q", sc.WorkspaceID, "ws-1")
		}
		if sc.Role != RoleOwner {
			mt.Errorf("Role = %q, want %q", sc.Role, RoleOwner)
		}
		if sc.Credential != CredentialStytchJWT {
			mt.Errorf("Credential = %v, want CredentialStytchJWT", sc.Credential)
		}
		if !sc.Grants(PermWorkspaceView) {
			mt.Error("owner should be granted PermWorkspaceView")
		}
	})
}

func TestResolver_Resolve_WorkspaceMismatch(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("membership resolves to a different workspace than the URL names", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, membershipDoc("m-1", "user-1", "ws-1", "member-1", 1)),
		)

		r := NewResolver(nil, newTestClock())
		id := resolvedIdentity{kind: CredentialStytchJWT, memberID: "member-1"}

		_, err := r.Resolve(context.Background(), id, "ws-OTHER")
		if !errors.Is(err, errUnlinkedMember) {
			mt.Errorf("err = %v, want errUnlinkedMember", err)
		}
	})
}

func TestResolver_Resolve_NoMembership(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no membership for this Stytch member ID", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch),
		)

		r := NewResolver(nil, newTestClock())
		id := resolvedIdentity{kind: CredentialStytchJWT, memberID: "member-ghost"}

		_, err := r.Resolve(context.Background(), id, "")
		if !errors.Is(err, errUnlinkedMember) {
			mt.Errorf("err = %v, want errUnlinkedMember", err)
		}
	})
}

func TestResolver_Resolve_NoAccountLink(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("membership exists but user holds no account link", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, membershipDoc("m-1", "user-1", "ws-1", "member-1", 1)),
			mtest.CreateCursorResponse(0, ns("account_links"), mtest.FirstBatch),
		)

		r := NewResolver(nil, newTestClock())
		id := resolvedIdentity{kind: CredentialStytchJWT, memberID: "member-1"}

		_, err := r.Resolve(context.Background(), id, "ws-1")
		if !errors.Is(err, errUnlinkedMember) {
			mt.Errorf("err = %v, want errUnlinkedMember", err)
		}
	})
}

// TestResolver_Resolve_CacheHitSkipsExpensiveJoins verifies the meaningful
// half of the cache contract: a cache hit skips the account_link, users, and
// workspace joins. The membership lookup itself still runs on every call
// (it's the only way to discover the current AuthorityVersion to compare
// against), but that lookup is a single indexed point query on
// uniq_membership_stytch_member — cheap enough that avoiding the three joins
// is the win that matters.
func TestResolver_Resolve_CacheHitSkipsExpensiveJoins(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("second Resolve with unchanged authority version hits the cache", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		ns := func(coll string) string { return mt.DB.Name() + "." + coll }

		// Two membership responses (one per Resolve call), but only one each
		// of account_links/users/workspaces. If the second call re-ran the
		// joins, it would exhaust this queue and mtest would fail the request.
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, membershipDoc("m-1", "user-1", "ws-1", "member-1", 1)),
			mtest.CreateCursorResponse(0, ns("account_links"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "link-1"},
				{Key: "user_id", Value: "user-1"},
			}),
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, bson.D{
				{Key: "_id", Value: "user-1"},
				{Key: "user_type", Value: "customer"},
				{Key: "status", Value: "active"},
			}),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, membershipDoc("m-1", "user-1", "ws-1", "member-1", 1)),
		)

		r := NewResolver(nil, newTestClock())
		id := resolvedIdentity{kind: CredentialStytchJWT, memberID: "member-1"}

		if _, err := r.Resolve(context.Background(), id, "ws-1"); err != nil {
			mt.Fatalf("first resolve: unexpected error: %v", err)
		}

		sc, err := r.Resolve(context.Background(), id, "ws-1")
		if err != nil {
			mt.Fatalf("second (cached) resolve: unexpected error: %v", err)
		}
		if sc.UserID != "user-1" {
			mt.Errorf("UserID = %q, want %q", sc.UserID, "user-1")
		}
	})
}
