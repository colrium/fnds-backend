package workspaces

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/configs"
	"backend/contextutil"
	"backend/jwtutil"
	"backend/responses"
	"backend/stytch"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// sessionCacheTTL bounds the worst-case staleness of an authority change when
// no Redis invalidation reaches this instance (Req 17.8).
const sessionCacheTTL = 30 * time.Second

// errUnlinkedMember is returned by Resolve when the credential validated but
// names no Fundis identity — a valid Stytch member with no matching
// Membership, or a Membership whose user holds no Account_Link (Req 16.4).
var errUnlinkedMember = errors.New("workspaces: no linked Fundis account")

// Resolver converts an inbound Authorization credential into a SessionContext.
// It is the Session_Resolver: authority is always re-derived from the
// Membership document, never trusted from a token claim (Req 16.6).
type Resolver struct {
	adapter *stytch.Adapter
	cache   *SharedSessionCache
	clock   stytch.Clock
}

// NewResolver creates a Resolver backed by the given Stytch adapter and clock.
// It owns its own SharedSessionCache (30s local TTL, Redis-backed when
// configs.RDB is set).
func NewResolver(adapter *stytch.Adapter, clock stytch.Clock) *Resolver {
	return &Resolver{
		adapter: adapter,
		cache:   NewSharedSessionCache(sessionCacheTTL, clock),
		clock:   clock,
	}
}

// resolvedIdentity is the credential-independent result of validating the
// Authorization header.
type resolvedIdentity struct {
	kind      CredentialKind
	memberID  string // set for CredentialStytchJWT / CredentialStytchToken
	sessionID string
	userID    string // set for CredentialLegacyJWT
}

// authenticateHeader runs the credential chain in order (Req 16.13, 21.10):
//  1. RS256 JWT issued by our Stytch project → verified locally, no network (Req 16.2, 20.5)
//  2. Opaque value → authenticated remotely against Stytch, one network call
//  3. HS256 JWT → jwtutil (legacy employer auth, migration window, Req 21.10)
//
// A failure at every step returns one error; the caller must respond with the
// single fixed 401 body regardless of which step failed, so no failure mode
// discloses which credential form was attempted.
func (r *Resolver) authenticateHeader(c echo.Context) (resolvedIdentity, error) {
	authHeader := c.Request().Header.Get("Authorization")
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || strings.TrimSpace(parts[1]) == "" {
		return resolvedIdentity{}, errors.New("workspaces: missing or malformed authorization header")
	}
	token := strings.TrimSpace(parts[1])

	if strings.Count(token, ".") == 2 {
		// JWT-shaped. Try the Stytch RS256 path first (no network), then the
		// legacy HS256 path.
		if sess, err := r.adapter.VerifySessionJWT(token); err == nil {
			return resolvedIdentity{kind: CredentialStytchJWT, memberID: sess.MemberID, sessionID: sess.SessionID}, nil
		}
		if claims, err := jwtutil.ValidateToken(token); err == nil {
			return resolvedIdentity{kind: CredentialLegacyJWT, userID: claims.UserID}, nil
		}
		return resolvedIdentity{}, errors.New("workspaces: token did not validate against any credential path")
	}

	// Not JWT-shaped: try as an opaque Stytch session token.
	var sess stytch.Session
	err := r.adapter.Do(c.Request().Context(), "AuthenticateSession", false, func(attemptCtx context.Context) error {
		var innerErr error
		sess, innerErr = r.adapter.Client().AuthenticateSession(attemptCtx, token)
		return innerErr
	})
	if err != nil {
		return resolvedIdentity{}, fmt.Errorf("workspaces: opaque session token did not authenticate: %w", err)
	}
	return resolvedIdentity{kind: CredentialStytchToken, memberID: sess.MemberID, sessionID: sess.SessionID}, nil
}

// Resolve produces the full SessionContext for a credential. workspaceID
// comes from the URL path when the route is workspace-scoped; pass "" for
// session-scoped routes (Req 8.10, 16.5).
//
// Member-to-user resolution (Req 5.8, 16.3): a Stytch Member is scoped to one
// Organization, so a Stytch credential resolves to (user_id, workspace_id) in
// one indexed read via memberships.stytch_member_id. A legacy credential
// already names the user_id directly, so it resolves via the
// (user_id, workspace_id) membership index instead.
func (r *Resolver) Resolve(ctx context.Context, id resolvedIdentity, workspaceID string) (SessionContext, error) {
	membershipsColl := configs.MI.DB.Collection("memberships")

	var membership Membership
	var err error
	switch id.kind {
	case CredentialStytchJWT, CredentialStytchToken:
		err = membershipsColl.FindOne(ctx, bson.M{"stytch_member_id": id.memberID}).Decode(&membership)
	case CredentialLegacyJWT:
		if workspaceID == "" {
			return SessionContext{}, errUnlinkedMember
		}
		err = membershipsColl.FindOne(ctx, bson.M{"user_id": id.userID, "workspace_id": workspaceID}).Decode(&membership)
	default:
		return SessionContext{}, errUnlinkedMember
	}
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return SessionContext{}, errUnlinkedMember
		}
		return SessionContext{}, fmt.Errorf("workspaces: resolve: find membership: %w", err)
	}

	// A workspace-scoped route's URL must name the same workspace the
	// credential resolved to; a mismatch means this credential does not
	// grant access to the workspace in the URL.
	if workspaceID != "" && membership.WorkspaceID != workspaceID {
		return SessionContext{}, errUnlinkedMember
	}

	if sc, ok := r.cache.Get(ctx, membership.UserID, membership.WorkspaceID, membership.AuthorityVersion); ok {
		return sc, nil
	}

	// Account_Link existence check (Req 16.4): a Membership with no
	// corresponding Account_Link names no Stytch-authenticated Fundis user.
	linkErr := configs.MI.DB.Collection("account_links").FindOne(ctx, bson.M{"user_id": membership.UserID}).Err()
	if linkErr != nil {
		if errors.Is(linkErr, mongo.ErrNoDocuments) {
			return SessionContext{}, errUnlinkedMember
		}
		return SessionContext{}, fmt.Errorf("workspaces: resolve: check account link: %w", linkErr)
	}

	user, err := users.GetUserByID(ctx, membership.UserID)
	if err != nil {
		return SessionContext{}, fmt.Errorf("workspaces: resolve: get user: %w", err)
	}
	if user == nil {
		return SessionContext{}, errUnlinkedMember
	}

	var ws Workspace
	if err := configs.MI.DB.Collection("workspaces").FindOne(ctx, bson.M{"_id": membership.WorkspaceID}).Decode(&ws); err != nil {
		return SessionContext{}, fmt.Errorf("workspaces: resolve: find workspace: %w", err)
	}

	sc := SessionContext{
		UserID:              membership.UserID,
		UserStatus:          user.Status,
		WorkspaceID:         membership.WorkspaceID,
		MembershipID:        membership.ID,
		MembershipStatus:    membership.Status,
		Role:                membership.Role,
		Permissions:         setOf(GrantedPermissions(membership.Role)...),
		AuthorityVersion:    membership.AuthorityVersion,
		DeletionState:       ws.DeletionState,
		DeletionEffectiveAt: ws.DeletionEffectiveAt,
		Credential:          id.kind,
		StytchMemberID:      membership.StytchMemberID,
		StytchSessionID:     id.sessionID,
		ResolvedAt:          r.clock.Now(),
	}

	r.cache.Set(ctx, sc)
	return sc, nil
}

// unauthorized is the single fixed body every credential-chain failure
// returns, so no failure mode discloses which path was tried (Req 16.13).
func unauthorized(c echo.Context) error {
	return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
}

func (r *Resolver) respondUnlinked(c echo.Context) error {
	return responses.SendErrorWithCode(c, http.StatusUnauthorized,
		"unlinked_member", "no linked Fundis account", "", "", nil)
}

// Authenticate is middleware for session-scoped routes (no workspace
// requirement): GET/POST /workspaces, POST /workspaces/:workspaceID/switch.
func (r *Resolver) Authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, err := r.authenticateHeader(c)
		if err != nil {
			return unauthorized(c)
		}
		sc, err := r.Resolve(c.Request().Context(), id, "")
		if err != nil {
			if errors.Is(err, errUnlinkedMember) {
				return r.respondUnlinked(c)
			}
			return unauthorized(c)
		}
		SetSessionContext(c, sc)
		contextutil.SetUser(c, sc.UserID)
		return next(c)
	}
}

// RequireSession is middleware for workspace-scoped ScopedGroup routes. It
// reads :workspaceID from the URL and requires the credential to resolve to
// exactly that workspace. Runs before RequirePermission, which reads the
// SessionContext this middleware sets.
func (r *Resolver) RequireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		workspaceID := c.Param("workspaceID")
		id, err := r.authenticateHeader(c)
		if err != nil {
			return unauthorized(c)
		}
		sc, err := r.Resolve(c.Request().Context(), id, workspaceID)
		if err != nil {
			if errors.Is(err, errUnlinkedMember) {
				return r.respondUnlinked(c)
			}
			return unauthorized(c)
		}
		SetSessionContext(c, sc)
		contextutil.SetUser(c, sc.UserID)
		return next(c)
	}
}
