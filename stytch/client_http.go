package stytch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/stytchauth/stytch-go/v18/stytch/b2b/b2bstytchapi"
	"github.com/stytchauth/stytch-go/v18/stytch/b2b/discovery"
	discoveryorgs "github.com/stytchauth/stytch-go/v18/stytch/b2b/discovery/organizations"
	magiclinksdiscovery "github.com/stytchauth/stytch-go/v18/stytch/b2b/magiclinks/discovery"
	emaildiscovery "github.com/stytchauth/stytch-go/v18/stytch/b2b/magiclinks/email/discovery"
	oauthdiscovery "github.com/stytchauth/stytch-go/v18/stytch/b2b/oauth/discovery"
	orgs "github.com/stytchauth/stytch-go/v18/stytch/b2b/organizations"
	"github.com/stytchauth/stytch-go/v18/stytch/b2b/organizations/members"
	b2bsessions "github.com/stytchauth/stytch-go/v18/stytch/b2b/sessions"
	"github.com/stytchauth/stytch-go/v18/stytch/config"
	"github.com/stytchauth/stytch-go/v18/stytch/stytcherror"
)

// defaultJWKSFetchTimeout bounds a single JWKS document fetch when
// Config.JWKSFetchTimeout is unset. It is deliberately short: the fetch happens
// under the jwksCache write lock, so it is a hard ceiling on how long session
// verification can be blocked platform-wide.
const defaultJWKSFetchTimeout = 10 * time.Second

// maxJWKSFetchTimeout is the upper bound applyDefaults clamps
// Config.JWKSFetchTimeout to. Anything larger falls back to the default rather
// than being honoured, since a misconfigured value here stalls session
// verification for every workspace.
const maxJWKSFetchTimeout = 30 * time.Second

// jwksFetchTimeoutFor resolves Config.JWKSFetchTimeout to an effective value.
// applyDefaults uses this too, so a config that reaches FetchJWKS without
// passing through the Adapter is bounded identically.
func jwksFetchTimeoutFor(cfg Config) time.Duration {
	if cfg.JWKSFetchTimeout <= 0 || cfg.JWKSFetchTimeout > maxJWKSFetchTimeout {
		return defaultJWKSFetchTimeout
	}
	return cfg.JWKSFetchTimeout
}

// HTTPClient is the production Client implementation, wrapping
// github.com/stytchauth/stytch-go/v18's B2B API. It is confined to this one
// file so Client stays the only seam other packages (and every test) depend
// on. Every method delegates to the SDK and translates SDK/HTTP errors into
// this package's *Error/Kind so Adapter.Do's classification and retry logic
// works identically against the real API and against Fake.
type HTTPClient struct {
	api         *b2bstytchapi.API
	publicToken string
	environment string
}

// NewHTTPClient builds an HTTPClient from cfg. JWKS initialization is
// skipped here — the Adapter owns JWKS fetching and caching via
// NewWithJWKSFetch/FetchJWKS below, so the SDK's own background JWKS
// keyfunc would be redundant and would hold an extra goroutine open for
// the life of the process.
func NewHTTPClient(cfg Config) (*HTTPClient, error) {
	if cfg.ProjectID == "" || cfg.Secret == "" {
		return nil, errors.New("stytch: ProjectID and Secret are required")
	}

	opts := []b2bstytchapi.Option{b2bstytchapi.WithSkipJWKSInitialization()}
	if cfg.BaseURL != "" {
		opts = append(opts, b2bstytchapi.WithBaseURI(cfg.BaseURL))
	}

	api, err := b2bstytchapi.NewClient(cfg.ProjectID, cfg.Secret, opts...)
	if err != nil {
		return nil, fmt.Errorf("stytch: construct client: %w", err)
	}

	return &HTTPClient{api: api, publicToken: cfg.PublicToken, environment: cfg.Environment}, nil
}

// FetchJWKS retrieves the B2B session JWKS for cfg.ProjectID over HTTP, in
// the shape stytch.Adapter's jwksCache expects (the raw JWKS document
// bytes). Pass this to NewWithJWKSFetch. The URL follows the same pattern
// the SDK itself uses internally for its own (skipped) JWKS keyfunc:
// {baseURI}/v1/b2b/sessions/jwks/{projectID}.
func FetchJWKS(cfg Config) func() ([]byte, error) {
	baseURI := string(config.BaseURILive)
	if cfg.Environment == "test" {
		baseURI = string(config.BaseURITest)
	}
	if cfg.BaseURL != "" {
		baseURI = cfg.BaseURL
	}
	jwksURL := fmt.Sprintf("%s/v1/b2b/sessions/jwks/%s", baseURI, cfg.ProjectID)

	// The fetch runs while jwksCache holds its write lock, so an unbounded
	// request would stall every concurrent VerifySessionJWT call behind it.
	// http.DefaultClient has no timeout; this one does. Callers pass the raw
	// Config here (it does not go through applyDefaults), so the same clamp is
	// applied locally.
	timeout := jwksFetchTimeoutFor(cfg)

	// The client timeout covers the whole exchange including reading the body;
	// the request context is what makes the in-flight call abandonable. Both
	// use the same bound so neither can outlive it.
	client := &http.Client{Timeout: timeout}

	return func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("stytch: JWKS fetch: unexpected status %d", resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
}

// classify maps an SDK error to this package's *Error. Every Client method
// below returns the result of this function rather than the raw SDK error,
// so Adapter.Do's Kind-based retry/classification logic (Req 20.2-20.4)
// applies uniformly regardless of which operation failed.
func classify(op string, err error) error {
	if err == nil {
		return nil
	}
	var stytchErr stytcherror.Error
	if errors.As(err, &stytchErr) {
		return &Error{
			Kind:       kindForStatus(stytchErr.StatusCode),
			Op:         op,
			Status:     stytchErr.StatusCode,
			StytchCode: string(stytchErr.ErrorType),
			err:        err,
		}
	}
	// Network error, timeout, or anything else the SDK didn't wrap as a
	// structured Stytch error — treat as unavailable so Adapter.Do retries it.
	return &Error{Kind: KindUnavailable, Op: op, err: err}
}

func kindForStatus(status int) Kind {
	switch {
	case status == http.StatusNotFound:
		return KindNotFound
	case status == http.StatusConflict:
		return KindConflict
	case status == http.StatusTooManyRequests:
		return KindRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return KindUnauthenticated
	case status >= 400 && status < 500:
		return KindInvalidRequest
	case status >= 500:
		return KindUnavailable
	default:
		return KindUnavailable
	}
}

// --- Discovery ---

func (c *HTTPClient) DiscoveryEmailSend(ctx context.Context, in DiscoveryEmailSendInput) error {
	_, err := c.api.MagicLinks.Email.Discovery.Send(ctx, &emaildiscovery.SendParams{
		EmailAddress:         in.Email,
		DiscoveryRedirectURL: in.RedirectURL,
	})
	return classify("DiscoveryEmailSend", err)
}

func (c *HTTPClient) DiscoveryAuthenticate(ctx context.Context, token string) (DiscoveryResult, error) {
	resp, err := c.api.MagicLinks.Discovery.Authenticate(ctx, &magiclinksdiscovery.AuthenticateParams{
		DiscoveryMagicLinksToken: token,
	})
	if err != nil {
		return DiscoveryResult{}, classify("DiscoveryAuthenticate", err)
	}
	return toDiscoveryResult(resp.IntermediateSessionToken, resp.EmailAddress, resp.DiscoveredOrganizations), nil
}

func (c *HTTPClient) OAuthDiscoveryStartURL(provider, redirectURL string) (string, error) {
	// Stytch does not expose an SDK call for this — the frontend/backend
	// builds the redirect URL directly against Stytch's public OAuth
	// discovery start endpoint, keyed by the project's public token.
	base := "https://test.stytch.com"
	if c.environment == "live" {
		base = "https://api.stytch.com"
	}
	// redirectURL routinely carries its own query string, so it must be
	// percent-encoded or Stytch receives a truncated/mangled parameter.
	query := neturl.Values{}
	query.Set("public_token", c.publicToken)
	query.Set("discovery_redirect_url", redirectURL)
	return fmt.Sprintf("%s/v1/b2b/public/oauth/%s/discovery/start?%s",
		base, neturl.PathEscape(provider), query.Encode()), nil
}

func (c *HTTPClient) OAuthDiscoveryAuthenticate(ctx context.Context, token string) (DiscoveryResult, error) {
	resp, err := c.api.OAuth.Discovery.Authenticate(ctx, &oauthdiscovery.AuthenticateParams{
		DiscoveryOAuthToken: token,
	})
	if err != nil {
		return DiscoveryResult{}, classify("OAuthDiscoveryAuthenticate", err)
	}
	return toDiscoveryResult(resp.IntermediateSessionToken, resp.EmailAddress, resp.DiscoveredOrganizations), nil
}

// --- Organizations ---

func (c *HTTPClient) CreateOrganizationFromDiscovery(ctx context.Context, in CreateOrgInput) (CreateOrgResult, error) {
	resp, err := c.api.Discovery.Organizations.Create(ctx, &discoveryorgs.CreateParams{
		IntermediateSessionToken: in.IntermediateSessionToken,
		OrganizationName:         in.Name,
		OrganizationSlug:         in.Slug,
		EmailJITProvisioning:     in.EmailJITProvisioning,
		EmailInvites:             in.EmailInvites,
		// IdempotencyKey has no Stytch API equivalent; Fundis's own
		// onboarding_states record (which stores the Stytch organization_id
		// before this call returns) is what makes retrying this call safe.
	})
	if err != nil {
		return CreateOrgResult{}, classify("CreateOrganizationFromDiscovery", err)
	}
	return CreateOrgResult{
		OrganizationID: resp.Organization.OrganizationID,
		MemberID:       resp.MemberID,
		SessionJWT:     resp.SessionJWT,
		SessionToken:   resp.SessionToken,
	}, nil
}

func (c *HTTPClient) UpdateOrganization(ctx context.Context, orgID string, in UpdateOrgInput) error {
	params := &orgs.UpdateParams{OrganizationID: orgID}
	if in.Name != "" {
		params.OrganizationName = in.Name
	}
	if in.Slug != "" {
		params.OrganizationSlug = in.Slug
	}
	_, err := c.api.Organizations.Update(ctx, params)
	return classify("UpdateOrganization", err)
}

func (c *HTTPClient) DeleteOrganization(ctx context.Context, orgID string) error {
	_, err := c.api.Organizations.Delete(ctx, &orgs.DeleteParams{OrganizationID: orgID})
	return classify("DeleteOrganization", err)
}

func (c *HTTPClient) GetOrganization(ctx context.Context, orgID string) (Organization, error) {
	resp, err := c.api.Organizations.Get(ctx, &orgs.GetParams{OrganizationID: orgID})
	if err != nil {
		return Organization{}, classify("GetOrganization", err)
	}
	return Organization{
		OrganizationID: resp.Organization.OrganizationID,
		Name:           resp.Organization.OrganizationName,
		Slug:           resp.Organization.OrganizationSlug,
	}, nil
}

func (c *HTTPClient) ListOrganizations(ctx context.Context, cursor string) (OrganizationPage, error) {
	resp, err := c.api.Organizations.Search(ctx, &orgs.SearchParams{Cursor: cursor})
	if err != nil {
		return OrganizationPage{}, classify("ListOrganizations", err)
	}
	page := OrganizationPage{NextCursor: resp.ResultsMetadata.NextCursor}
	for _, o := range resp.Organizations {
		page.Organizations = append(page.Organizations, Organization{
			OrganizationID: o.OrganizationID,
			Name:           o.OrganizationName,
			Slug:           o.OrganizationSlug,
		})
	}
	return page, nil
}

// --- Members ---

func (c *HTTPClient) CreateMember(ctx context.Context, in CreateMemberInput) (Member, error) {
	resp, err := c.api.Organizations.Members.Create(ctx, &members.CreateParams{
		OrganizationID: in.OrganizationID,
		EmailAddress:   in.Email,
		Name:           in.Name,
		Roles:          in.Roles,
	})
	if err != nil {
		return Member{}, classify("CreateMember", err)
	}
	return Member{
		MemberID:       resp.MemberID,
		OrganizationID: in.OrganizationID,
		Email:          resp.Member.EmailAddress,
		Name:           resp.Member.Name,
	}, nil
}

func (c *HTTPClient) UpdateMemberRoles(ctx context.Context, orgID, memberID string, roles []string) error {
	_, err := c.api.Organizations.Members.Update(ctx, &members.UpdateParams{
		OrganizationID: orgID,
		MemberID:       memberID,
		Roles:          &roles,
	})
	return classify("UpdateMemberRoles", err)
}

func (c *HTTPClient) DeleteMember(ctx context.Context, orgID, memberID string) error {
	_, err := c.api.Organizations.Members.Delete(ctx, &members.DeleteParams{
		OrganizationID: orgID,
		MemberID:       memberID,
	})
	return classify("DeleteMember", err)
}

func (c *HTTPClient) SearchMembersByEmail(ctx context.Context, orgID, email string) ([]Member, error) {
	resp, err := c.api.Organizations.Members.Search(ctx, &members.SearchParams{
		OrganizationIds: []string{orgID},
		Query: &orgs.SearchQuery{
			Operator: orgs.SearchQueryOperatorAND,
			Operands: []map[string]any{
				{"filter_name": "member_emails", "filter_value": []string{email}},
			},
		},
	})
	if err != nil {
		return nil, classify("SearchMembersByEmail", err)
	}
	result := make([]Member, 0, len(resp.Members))
	for _, m := range resp.Members {
		result = append(result, Member{
			MemberID:       m.MemberID,
			OrganizationID: m.OrganizationID,
			Email:          m.EmailAddress,
			Name:           m.Name,
		})
	}
	return result, nil
}

// --- Sessions ---

func (c *HTTPClient) AuthenticateSession(ctx context.Context, sessionToken string) (Session, error) {
	resp, err := c.api.Sessions.Authenticate(ctx, &b2bsessions.AuthenticateParams{SessionToken: sessionToken})
	if err != nil {
		return Session{}, classify("AuthenticateSession", err)
	}
	return toSession(resp.MemberSession, resp.Member.EmailAddress, resp.SessionJWT, resp.SessionToken), nil
}

func (c *HTTPClient) ExchangeSession(ctx context.Context, sessionToken, targetOrgID string) (Session, error) {
	resp, err := c.api.Sessions.Exchange(ctx, &b2bsessions.ExchangeParams{
		OrganizationID: targetOrgID,
		SessionToken:   sessionToken,
	})
	if err != nil {
		return Session{}, classify("ExchangeSession", err)
	}
	return Session{
		MemberID:       resp.MemberID,
		OrganizationID: resp.Organization.OrganizationID,
		Email:          resp.Member.EmailAddress,
		EmailVerified:  true,
		SessionJWT:     resp.SessionJWT,
		SessionToken:   resp.SessionToken,
	}, nil
}

func (c *HTTPClient) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := c.api.Sessions.Revoke(ctx, &b2bsessions.RevokeParams{MemberSessionID: sessionID})
	return classify("RevokeSession", err)
}

func (c *HTTPClient) RevokeMemberSessions(ctx context.Context, orgID, memberID string) error {
	resp, err := c.api.Sessions.Get(ctx, &b2bsessions.GetParams{OrganizationID: orgID, MemberID: memberID})
	if err != nil {
		return classify("RevokeMemberSessions", err)
	}
	// Revoking is idempotent per session, so a failure on one session must not
	// leave later sessions alive: attempt every one, then report. A retry of
	// the whole call is safe and only re-attempts what is still live.
	var revokeErrs []error
	for _, sess := range resp.MemberSessions {
		if _, revokeErr := c.api.Sessions.Revoke(ctx, &b2bsessions.RevokeParams{MemberSessionID: sess.MemberSessionID}); revokeErr != nil {
			slog.ErrorContext(ctx, "stytch: failed to revoke member session",
				"session_id", sess.MemberSessionID,
				"error", revokeErr.Error(),
			)
			revokeErrs = append(revokeErrs, revokeErr)
		}
	}
	if len(revokeErrs) > 0 {
		// classify's errors.As walks a joined tree, so the resulting Kind
		// still reflects the underlying SDK error rather than a generic one.
		return classify("RevokeMemberSessions", errors.Join(revokeErrs...))
	}
	return nil
}

// --- helpers ---

func toDiscoveryResult(intermediateToken, email string, discovered []discovery.DiscoveredOrganization) DiscoveryResult {
	result := DiscoveryResult{
		IntermediateSessionToken: intermediateToken,
		Email:                    email,
		// A successful discovery authenticate is Stytch's own proof that the
		// factor (magic link click, OAuth grant) was completed, which is what
		// Stytch treats as email verification for this flow.
		EmailVerified: true,
	}
	for _, d := range discovered {
		entry := DiscoveredOrganization{}
		if d.Organization != nil {
			entry.OrganizationID = d.Organization.OrganizationID
			entry.OrganizationName = d.Organization.OrganizationName
		}
		if d.Membership != nil && d.Membership.Member != nil {
			result.MemberID = d.Membership.Member.MemberID
		}
		result.DiscoveredOrganizations = append(result.DiscoveredOrganizations, entry)
	}
	return result
}

func toSession(ms b2bsessions.MemberSession, email, sessionJWT, sessionToken string) Session {
	var expiresAt time.Time
	if ms.ExpiresAt != nil {
		expiresAt = *ms.ExpiresAt
	}
	return Session{
		MemberID:       ms.MemberID,
		OrganizationID: ms.OrganizationID,
		SessionID:      ms.MemberSessionID,
		Email:          email,
		EmailVerified:  true,
		ExpiresAt:      expiresAt,
		SessionJWT:     sessionJWT,
		SessionToken:   sessionToken,
		Roles:          ms.Roles,
	}
}
