package workspaces

import "github.com/labstack/echo/v4"

// InvitationHandlers bundles the seven invitation-related HTTP handlers.
//
// The handlers themselves are implemented in the invitations package, not
// here, because invitations.Service depends on workspaces types (Role,
// Membership, MembershipService, ...) — workspaces importing invitations
// back would be a circular package dependency. echo.HandlerFunc is
// `func(echo.Context) error`, which carries no invitations-specific types in
// its signature, so a struct of these function values can cross the package
// boundary with zero import of "backend/invitations" from this package.
//
// main.go constructs the concrete handlers via invitations.NewHTTPHandlers
// and passes the result in on RouteConfig.Invitations.
type InvitationHandlers struct {
	// Resolve serves GET /invitations/:token (public).
	Resolve echo.HandlerFunc
	// Decline serves POST /invitations/:token/decline (public).
	Decline echo.HandlerFunc
	// Accept serves POST /invitations/:token/accept (public).
	Accept echo.HandlerFunc
	// List serves GET /workspaces/:workspaceID/invitations (workspace-scoped).
	List echo.HandlerFunc
	// Create serves POST /workspaces/:workspaceID/invitations (workspace-scoped).
	Create echo.HandlerFunc
	// Resend serves POST /workspaces/:workspaceID/invitations/:id/resend (workspace-scoped).
	Resend echo.HandlerFunc
	// Revoke serves DELETE /workspaces/:workspaceID/invitations/:id (workspace-scoped).
	Revoke echo.HandlerFunc
}
