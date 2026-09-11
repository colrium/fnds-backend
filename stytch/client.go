package stytch

import (
	"context"
	"time"
)

// Client is the seam between the domain and the Stytch B2B API. The HTTP
// implementation wraps stytch-go/v18; the fake implements the same interface
// in-process with no network and no goroutine.
type Client interface {
	// --- Discovery — employer signup (Req 1.1, 1.2) ---

	// DiscoveryEmailSend sends a magic-link email for the discovery flow.
	DiscoveryEmailSend(ctx context.Context, in DiscoveryEmailSendInput) error

	// DiscoveryAuthenticate redeems a discovery token (magic-link or intermediate).
	DiscoveryAuthenticate(ctx context.Context, token string) (DiscoveryResult, error)

	// OAuthDiscoveryStartURL returns the redirect URL for starting an OAuth
	// discovery flow for the given provider (e.g. "google").
	OAuthDiscoveryStartURL(provider, redirectURL string) (string, error)

	// OAuthDiscoveryAuthenticate redeems an OAuth discovery token.
	OAuthDiscoveryAuthenticate(ctx context.Context, token string) (DiscoveryResult, error)

	// ExchangeIntermediateSession exchanges a discovery intermediate-session
	// token for a full Member session in an existing Organization.
	ExchangeIntermediateSession(ctx context.Context, intermediateToken, targetOrgID string) (Session, error)

	// --- Organizations (Req 2.1, 2.13, 2.16, 3.8, 18.8) ---

	// CreateOrganizationFromDiscovery creates a Stytch Organization from the
	// discovery intermediate session.
	CreateOrganizationFromDiscovery(ctx context.Context, in CreateOrgInput) (CreateOrgResult, error)

	// CreateOrganization creates a plain Stytch Organization with no Member
	// and no discovery session involved. Used when an already-authenticated
	// member creates an additional Workspace (Req 15.12): unlike signup,
	// there is no discovery intermediate session to reuse, so the
	// Organization and the caller's Member (via CreateMember) are created as
	// two separate calls instead of the combined discovery Create call.
	CreateOrganization(ctx context.Context, in CreateOrganizationInput) (CreateOrganizationResult, error)

	// UpdateOrganization updates mutable fields on a Stytch Organization.
	UpdateOrganization(ctx context.Context, orgID string, in UpdateOrgInput) error

	// DeleteOrganization deletes a Stytch Organization.
	DeleteOrganization(ctx context.Context, orgID string) error

	// GetOrganization retrieves a Stytch Organization by ID.
	GetOrganization(ctx context.Context, orgID string) (Organization, error)

	// ListOrganizations pages through all Stytch Organizations. Pass an empty
	// cursor for the first page.
	ListOrganizations(ctx context.Context, cursor string) (OrganizationPage, error)

	// --- Members (Req 11.9, 13.3, 13.12, 7.15, 19.8) ---

	// CreateMember creates a Stytch Member in the given Organization.
	CreateMember(ctx context.Context, in CreateMemberInput) (Member, error)

	// UpdateMemberRoles updates the RBAC roles on a Stytch Member.
	UpdateMemberRoles(ctx context.Context, orgID, memberID string, roles []string) error

	// DeleteMember removes a Stytch Member from a Stytch Organization.
	DeleteMember(ctx context.Context, orgID, memberID string) error

	// SearchMembersByEmail searches for Members by email within an Organization.
	SearchMembersByEmail(ctx context.Context, orgID, email string) ([]Member, error)

	// --- Sessions (Req 15.9, 16.2, 17.3, 17.5, 17.6) ---

	// AuthenticateSession authenticates an opaque session token and returns the
	// resolved session.
	AuthenticateSession(ctx context.Context, sessionToken string) (Session, error)

	// ExchangeSession exchanges a session token for a new session scoped to a
	// different Organization.
	ExchangeSession(ctx context.Context, sessionToken, targetOrgID string) (Session, error)

	// RevokeSession revokes a single session by ID.
	RevokeSession(ctx context.Context, sessionID string) error

	// RevokeMemberSessions revokes all sessions for a Member in an Organization.
	RevokeMemberSessions(ctx context.Context, orgID, memberID string) error
}

// DiscoveryEmailSendInput carries the parameters for DiscoveryEmailSend.
type DiscoveryEmailSendInput struct {
	// Email is the normalized email address to send the magic link to.
	Email string

	// RedirectURL is where the magic link redirects after click.
	RedirectURL string
}

// DiscoveryResult is returned by DiscoveryAuthenticate and OAuthDiscoveryAuthenticate.
type DiscoveryResult struct {
	// IntermediateSessionToken is the short-lived token used to create or join
	// an Organization.
	IntermediateSessionToken string

	// Email is the authenticated email address.
	Email string

	// EmailVerified reports whether Stytch verified the email.
	EmailVerified bool

	// Name is the display name reported by Stytch, if any.
	Name string

	// MemberID is the Stytch Member identifier, if already associated.
	MemberID string

	// DiscoveredOrganizations lists the Organizations the member can join.
	DiscoveredOrganizations []DiscoveredOrganization
}

// DiscoveredOrganization is one entry in the discovery result.
type DiscoveredOrganization struct {
	OrganizationID   string
	OrganizationName string
}

// Organization provisioning settings fixed for every Fundis Workspace
// (Req 2.13, D6).
const (
	// EmailJITNotAllowed disables email-domain auto-join entirely, which is what
	// Req 2.13's "the value that disables domain-based auto-join" means and what
	// D6 requires: Kenyan SMEs overwhelmingly sign up with gmail.com addresses,
	// so any domain-based join would let strangers into each other's Workspaces.
	// Membership is by invitation only.
	//
	// NOT "RESTRICTED". Restricted does not mean "locked down" — it means "JIT is
	// allowed, but only for domains on email_allowed_domains". Sending it with no
	// allowed-domain list is self-contradictory, and Stytch rejects the create
	// with 400 invalid_restricted_email_setting. Stytch also refuses to accept
	// public domains such as gmail.com in that list at all, so RESTRICTED could
	// never express this policy even with a list.
	EmailJITNotAllowed = "NOT_ALLOWED"

	// EmailInvitesAllAllowed permits invitations to any email domain, which is
	// the other half of Req 2.13: invitation-only membership is worthless if the
	// invitee's gmail address cannot be invited.
	EmailInvitesAllAllowed = "ALL_ALLOWED"
)

// CreateOrgInput carries the parameters for CreateOrganizationFromDiscovery.
// It includes the two settings Req 2.13 fixes for every Workspace and the
// Fundis-side idempotency value of Req 3.12.
type CreateOrgInput struct {
	// IntermediateSessionToken from the discovery flow.
	IntermediateSessionToken string

	// Name is the Workspace name (used as the Organization display name).
	Name string

	// Slug is the URL-safe identifier for the Organization.
	Slug string

	// EmailJITProvisioning is always EmailJITNotAllowed — see its doc comment.
	EmailJITProvisioning string

	// EmailInvites is always "ALL_ALLOWED" — permits invitations to any email domain.
	EmailInvites string

	// IdempotencyKey is "onboarding:" + onboarding_state _id (Req 3.12).
	IdempotencyKey string
}

// CreateOrgResult is returned by CreateOrganizationFromDiscovery.
type CreateOrgResult struct {
	// OrganizationID is the Stytch Organization identifier.
	OrganizationID string

	// MemberID is the creating Member's identifier in the new Organization.
	MemberID string

	// SessionJWT is the session JWT for the new Organization.
	SessionJWT string

	// SessionToken is the opaque session token for the new Organization.
	SessionToken string
}

// CreateOrganizationInput carries the parameters for the plain
// CreateOrganization call (no discovery session, no Member created).
type CreateOrganizationInput struct {
	// Name is the Workspace name (used as the Organization display name).
	Name string

	// Slug is the URL-safe identifier for the Organization.
	Slug string

	// EmailJITProvisioning is always EmailJITNotAllowed — see its doc comment.
	EmailJITProvisioning string

	// EmailInvites is always "ALL_ALLOWED" — permits invitations to any email domain.
	EmailInvites string
}

// CreateOrganizationResult is returned by CreateOrganization.
type CreateOrganizationResult struct {
	// OrganizationID is the Stytch Organization identifier.
	OrganizationID string
}

// UpdateOrgInput carries the mutable fields for UpdateOrganization.
type UpdateOrgInput struct {
	// Name is the new display name, if non-empty.
	Name string

	// Slug is the new slug, if non-empty.
	Slug string
}

// Organization represents a Stytch Organization as returned by GetOrganization
// and ListOrganizations.
type Organization struct {
	// OrganizationID is the Stytch identifier.
	OrganizationID string

	// Name is the Organization display name.
	Name string

	// Slug is the URL-safe identifier.
	Slug string
}

// OrganizationPage is a page of Organizations from ListOrganizations.
type OrganizationPage struct {
	// Organizations is the slice of results for this page.
	Organizations []Organization

	// NextCursor is the cursor for the next page, or empty if this is the last page.
	NextCursor string
}

// CreateMemberInput carries the parameters for CreateMember.
type CreateMemberInput struct {
	// OrganizationID is the Stytch Organization to create the Member in.
	OrganizationID string

	// Email is the Member's email address.
	Email string

	// Name is the Member's display name.
	Name string

	// Roles are the initial RBAC roles to assign.
	Roles []string
}

// Member represents a Stytch Member as returned by CreateMember and
// SearchMembersByEmail.
type Member struct {
	// MemberID is the Stytch Member identifier.
	MemberID string

	// OrganizationID is the Organization the Member belongs to.
	OrganizationID string

	// Email is the Member's email address.
	Email string

	// Name is the Member's display name.
	Name string
}

// Session is the adapter's credential-agnostic result from session operations.
type Session struct {
	// MemberID is the Stytch Member identifier.
	MemberID string

	// OrganizationID is the Stytch Organization the session is scoped to.
	OrganizationID string

	// SessionID is the Stytch session identifier.
	SessionID string

	// Email is the Member's email address.
	Email string

	// EmailVerified reports whether the email is verified.
	EmailVerified bool

	// ExpiresAt is when the session expires.
	ExpiresAt time.Time

	// SessionJWT is the RS256 session JWT returned by Stytch on exchange or
	// authentication. Populated by ExchangeSession and AuthenticateSession.
	SessionJWT string

	// SessionToken is the opaque session token returned by Stytch.
	SessionToken string

	// Roles is populated for observability only. Session_Resolver ignores it
	// (Req 16.6). It is deliberately not typed as workspaces.Role so no caller
	// can mistake it for authority.
	Roles []string
}
