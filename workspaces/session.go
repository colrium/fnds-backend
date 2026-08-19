package workspaces

import (
	"time"

	"github.com/labstack/echo/v4"
)

// CredentialKind identifies which authentication path was used.
type CredentialKind uint8

const (
	CredentialStytchJWT   CredentialKind = iota // verified locally against the JWKS
	CredentialStytchToken                       // opaque; authenticated remotely
	CredentialLegacyJWT                         // jwtutil HS256, migration window
)

// SessionContext is the Workspace_Session_Context. It is immutable once
// resolved. Authorization fields (Role, Permissions, MembershipStatus,
// AuthorityVersion, DeletionState) are read from the Membership document, not
// from a token claim (Req 16.6) — that's the property Req 16.6 protects.
// StytchMemberID and StytchSessionID are the exception: they come directly
// from the credential/token exchange and are refreshed on workspace switch,
// so a Stytch-side member or session ID rotation is only reflected here after
// the next switch or the cache's 30-second TTL expires.
type SessionContext struct {
	UserID              string
	UserStatus          string // users.status — Req 8.9
	WorkspaceID         string
	MembershipID        string
	MembershipStatus    MembershipStatus // active, suspended, or removed
	Role                Role
	Permissions         PermissionSet
	AuthorityVersion    int64
	DeletionState       DeletionState // Req 18.4
	DeletionEffectiveAt *time.Time    // when deletion_state == "scheduled"
	Credential          CredentialKind
	StytchMemberID      string
	StytchSessionID     string
	ResolvedAt          time.Time
}

// Grants reports whether this session holds the given Permission.
func (sc SessionContext) Grants(p Permission) bool {
	return sc.Permissions.has(p)
}

// sessionContextKey is the echo context key holding the resolved
// SessionContext. Echo's store is keyed by string, so SetSessionContext and
// FromEcho must agree on this exact value.
const sessionContextKey = "session_context"

// SetSessionContext stores the resolved SessionContext in the echo context.
func SetSessionContext(c echo.Context, sc SessionContext) {
	c.Set(sessionContextKey, sc)
}

// FromEcho reads the SessionContext the middleware stored. Handlers in
// jobrequests and subscriptions call this when they need the workspace scope.
func FromEcho(c echo.Context) (SessionContext, bool) {
	sc, ok := c.Get(sessionContextKey).(SessionContext)
	return sc, ok
}
