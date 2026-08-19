package stytch

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Call records one invocation of the fake for assertions.
type Call struct {
	Op   string
	Args any
	At   time.Time
	Err  error
}

// FailSpec configures a failure injection for a named operation at a specific attempt.
type FailSpec struct {
	Attempt    int // which attempt to fail (1-based)
	Kind       Kind
	Status     int
	RetryAfter time.Duration
}

// Fake implements Client with in-memory maps, no goroutine, and no network.
// It is shipped in the non-test build so other packages' tests can use it
// without a cyclic test-only import.
type Fake struct {
	mu    sync.Mutex
	Orgs  map[string]*FakeOrg
	Calls []Call
	Clock Clock

	// FailAt makes the Nth call to a named op fail with the given Kind.
	// Key is the operation name (e.g. "CreateOrganizationFromDiscovery").
	FailAt map[string][]FailSpec

	// SigningKey is a real RSA private key used to mint session JWTs that the
	// adapter's actual local verification path can verify.
	SigningKey *rsa.PrivateKey

	// kid is the key ID used in JWKS and JWT headers.
	kid string

	// callCounts tracks per-op invocation counts for FailAt matching.
	callCounts map[string]int
}

// FakeOrg is one Organization in the fake.
type FakeOrg struct {
	Organization Organization
	Members      map[string]Member  // memberID -> Member
	Sessions     map[string]Session // sessionID -> Session
}

// NewFake constructs a Fake with a 2048-bit RSA signing key.
func NewFake(clock Clock) *Fake {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("stytch.NewFake: generate RSA key: %v", err))
	}
	return &Fake{
		Orgs:       make(map[string]*FakeOrg),
		Calls:      nil,
		Clock:      clock,
		FailAt:     make(map[string][]FailSpec),
		SigningKey: key,
		kid:        uuid.New().String(),
		callCounts: make(map[string]int),
	}
}

// JWKS returns the RSA public key in standard JWKS JSON format.
func (f *Fake) JWKS() []byte {
	pub := &f.SigningKey.PublicKey
	nBytes := pub.N.Bytes()
	eBytes := big.NewInt(int64(pub.E)).Bytes()

	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": f.kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(nBytes),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}
	data, _ := json.Marshal(jwks)
	return data
}

// MintSessionJWT signs a JWT with the fake's RSA key carrying standard claims.
func (f *Fake) MintSessionJWT(memberID, orgID string, ttl time.Duration) string {
	now := f.Clock.Now()
	claims := jwt.MapClaims{
		"sub":    memberID,
		"org_id": orgID,
		"iss":    "stytch.com/project-test",
		"iat":    now.Unix(),
		"exp":    now.Add(ttl).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(f.SigningKey)
	if err != nil {
		panic(fmt.Sprintf("stytch.Fake.MintSessionJWT: sign: %v", err))
	}
	return signed
}

// CallCount returns how many times the named operation has been invoked.
func (f *Fake) CallCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCounts[op]
}

// Reset clears all recorded calls, call counts, organizations, and failure specs.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = nil
	f.callCounts = make(map[string]int)
	f.Orgs = make(map[string]*FakeOrg)
	f.FailAt = make(map[string][]FailSpec)
}

// --- internal helpers ---

// before checks FailAt for the current op+attempt and records the call.
// Returns an error if the op should fail on this attempt.
func (f *Fake) before(op string, args any) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.callCounts[op]++
	attempt := f.callCounts[op]

	var callErr error
	if specs, ok := f.FailAt[op]; ok {
		for _, spec := range specs {
			if spec.Attempt == attempt {
				callErr = &Error{
					Kind:       spec.Kind,
					Op:         op,
					Status:     spec.Status,
					RetryAfter: spec.RetryAfter,
				}
				break
			}
		}
	}

	f.Calls = append(f.Calls, Call{
		Op:   op,
		Args: args,
		At:   f.Clock.Now(),
		Err:  callErr,
	})

	return callErr
}

// --- Client interface implementation ---

func (f *Fake) DiscoveryEmailSend(_ context.Context, in DiscoveryEmailSendInput) error {
	if err := f.before("DiscoveryEmailSend", in); err != nil {
		return err
	}
	// No-op success: magic-link emails are not observable in the fake.
	return nil
}

func (f *Fake) DiscoveryAuthenticate(_ context.Context, token string) (DiscoveryResult, error) {
	if err := f.before("DiscoveryAuthenticate", token); err != nil {
		return DiscoveryResult{}, err
	}

	// Return a mock discovery result based on the token value.
	return DiscoveryResult{
		IntermediateSessionToken: "ist_" + token,
		Email:                    "user@example.com",
		EmailVerified:            true,
		Name:                     "Test User",
		MemberID:                 "member-" + uuid.New().String()[:8],
	}, nil
}

func (f *Fake) OAuthDiscoveryStartURL(provider, redirectURL string) (string, error) {
	if err := f.before("OAuthDiscoveryStartURL", map[string]string{
		"provider":    provider,
		"redirectURL": redirectURL,
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("https://stytch.com/oauth/%s?redirect=%s", provider, redirectURL), nil
}

func (f *Fake) OAuthDiscoveryAuthenticate(_ context.Context, token string) (DiscoveryResult, error) {
	if err := f.before("OAuthDiscoveryAuthenticate", token); err != nil {
		return DiscoveryResult{}, err
	}

	return DiscoveryResult{
		IntermediateSessionToken: "ist_oauth_" + token,
		Email:                    "oauth@example.com",
		EmailVerified:            true,
		Name:                     "OAuth User",
		MemberID:                 "member-" + uuid.New().String()[:8],
	}, nil
}

func (f *Fake) CreateOrganizationFromDiscovery(_ context.Context, in CreateOrgInput) (CreateOrgResult, error) {
	if err := f.before("CreateOrganizationFromDiscovery", in); err != nil {
		return CreateOrgResult{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	orgID := "organization-" + uuid.New().String()
	memberID := "member-" + uuid.New().String()
	sessionID := "session-" + uuid.New().String()

	org := &FakeOrg{
		Organization: Organization{
			OrganizationID: orgID,
			Name:           in.Name,
			Slug:           in.Slug,
		},
		Members:  make(map[string]Member),
		Sessions: make(map[string]Session),
	}

	// Add the creating member.
	org.Members[memberID] = Member{
		MemberID:       memberID,
		OrganizationID: orgID,
		Email:          "", // will be set by caller
		Name:           "",
	}

	// Store a session for the creating member.
	org.Sessions[sessionID] = Session{
		MemberID:       memberID,
		OrganizationID: orgID,
		SessionID:      sessionID,
		ExpiresAt:      f.Clock.Now().Add(time.Hour),
	}

	f.Orgs[orgID] = org

	sessionJWT := f.mintSessionJWTLocked(memberID, orgID, time.Hour)

	return CreateOrgResult{
		OrganizationID: orgID,
		MemberID:       memberID,
		SessionJWT:     sessionJWT,
		SessionToken:   "session_token_" + sessionID,
	}, nil
}

// mintSessionJWTLocked mints a JWT without acquiring the mutex (caller holds it).
func (f *Fake) mintSessionJWTLocked(memberID, orgID string, ttl time.Duration) string {
	now := f.Clock.Now()
	claims := jwt.MapClaims{
		"sub":    memberID,
		"org_id": orgID,
		"iss":    "stytch.com/project-test",
		"iat":    now.Unix(),
		"exp":    now.Add(ttl).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(f.SigningKey)
	if err != nil {
		panic(fmt.Sprintf("stytch.Fake.mintSessionJWTLocked: sign: %v", err))
	}
	return signed
}

func (f *Fake) UpdateOrganization(_ context.Context, orgID string, in UpdateOrgInput) error {
	if err := f.before("UpdateOrganization", map[string]any{
		"orgID": orgID,
		"input": in,
	}); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[orgID]
	if !ok {
		return &Error{Kind: KindNotFound, Op: "UpdateOrganization", Status: 404}
	}

	if in.Name != "" {
		org.Organization.Name = in.Name
	}
	if in.Slug != "" {
		org.Organization.Slug = in.Slug
	}
	return nil
}

func (f *Fake) DeleteOrganization(_ context.Context, orgID string) error {
	if err := f.before("DeleteOrganization", orgID); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Orgs[orgID]; !ok {
		return &Error{Kind: KindNotFound, Op: "DeleteOrganization", Status: 404}
	}
	delete(f.Orgs, orgID)
	return nil
}

func (f *Fake) GetOrganization(_ context.Context, orgID string) (Organization, error) {
	if err := f.before("GetOrganization", orgID); err != nil {
		return Organization{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[orgID]
	if !ok {
		return Organization{}, &Error{Kind: KindNotFound, Op: "GetOrganization", Status: 404}
	}
	return org.Organization, nil
}

func (f *Fake) ListOrganizations(_ context.Context, cursor string) (OrganizationPage, error) {
	if err := f.before("ListOrganizations", cursor); err != nil {
		return OrganizationPage{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var orgs []Organization
	for _, o := range f.Orgs {
		orgs = append(orgs, o.Organization)
	}

	return OrganizationPage{
		Organizations: orgs,
		NextCursor:    "", // single page for the fake
	}, nil
}

func (f *Fake) CreateMember(_ context.Context, in CreateMemberInput) (Member, error) {
	if err := f.before("CreateMember", in); err != nil {
		return Member{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[in.OrganizationID]
	if !ok {
		return Member{}, &Error{Kind: KindNotFound, Op: "CreateMember", Status: 404}
	}

	memberID := "member-" + uuid.New().String()
	m := Member{
		MemberID:       memberID,
		OrganizationID: in.OrganizationID,
		Email:          in.Email,
		Name:           in.Name,
	}
	org.Members[memberID] = m
	return m, nil
}

func (f *Fake) UpdateMemberRoles(_ context.Context, orgID, memberID string, roles []string) error {
	if err := f.before("UpdateMemberRoles", map[string]any{
		"orgID":    orgID,
		"memberID": memberID,
		"roles":    roles,
	}); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[orgID]
	if !ok {
		return &Error{Kind: KindNotFound, Op: "UpdateMemberRoles", Status: 404}
	}
	if _, ok := org.Members[memberID]; !ok {
		return &Error{Kind: KindNotFound, Op: "UpdateMemberRoles", Status: 404}
	}
	// Roles are stored on the Stytch side; in the fake we acknowledge the call.
	return nil
}

func (f *Fake) DeleteMember(_ context.Context, orgID, memberID string) error {
	if err := f.before("DeleteMember", map[string]string{
		"orgID":    orgID,
		"memberID": memberID,
	}); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[orgID]
	if !ok {
		return &Error{Kind: KindNotFound, Op: "DeleteMember", Status: 404}
	}
	if _, ok := org.Members[memberID]; !ok {
		return &Error{Kind: KindNotFound, Op: "DeleteMember", Status: 404}
	}
	delete(org.Members, memberID)
	return nil
}

func (f *Fake) SearchMembersByEmail(_ context.Context, orgID, email string) ([]Member, error) {
	if err := f.before("SearchMembersByEmail", map[string]string{
		"orgID": orgID,
		"email": email,
	}); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[orgID]
	if !ok {
		return nil, &Error{Kind: KindNotFound, Op: "SearchMembersByEmail", Status: 404}
	}

	var results []Member
	for _, m := range org.Members {
		if m.Email == email {
			results = append(results, m)
		}
	}
	return results, nil
}

func (f *Fake) AuthenticateSession(_ context.Context, sessionToken string) (Session, error) {
	if err := f.before("AuthenticateSession", sessionToken); err != nil {
		return Session{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Look up session across all orgs.
	for _, org := range f.Orgs {
		for _, sess := range org.Sessions {
			if "session_token_"+sess.SessionID == sessionToken {
				if f.Clock.Now().After(sess.ExpiresAt) {
					return Session{}, &Error{Kind: KindUnauthenticated, Op: "AuthenticateSession", Status: 401}
				}
				return sess, nil
			}
		}
	}
	return Session{}, &Error{Kind: KindUnauthenticated, Op: "AuthenticateSession", Status: 401}
}

func (f *Fake) ExchangeSession(_ context.Context, sessionToken, targetOrgID string) (Session, error) {
	if err := f.before("ExchangeSession", map[string]string{
		"sessionToken": sessionToken,
		"targetOrgID":  targetOrgID,
	}); err != nil {
		return Session{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Find the source session.
	var sourceSess *Session
	for _, org := range f.Orgs {
		for _, sess := range org.Sessions {
			if "session_token_"+sess.SessionID == sessionToken {
				s := sess
				sourceSess = &s
				break
			}
		}
		if sourceSess != nil {
			break
		}
	}
	if sourceSess == nil {
		return Session{}, &Error{Kind: KindUnauthenticated, Op: "ExchangeSession", Status: 401}
	}

	// Verify target org exists.
	targetOrg, ok := f.Orgs[targetOrgID]
	if !ok {
		return Session{}, &Error{Kind: KindNotFound, Op: "ExchangeSession", Status: 404}
	}

	// Create a new session in the target org.
	newSessionID := "session-" + uuid.New().String()
	newSess := Session{
		MemberID:       sourceSess.MemberID,
		OrganizationID: targetOrgID,
		SessionID:      newSessionID,
		Email:          sourceSess.Email,
		EmailVerified:  sourceSess.EmailVerified,
		ExpiresAt:      f.Clock.Now().Add(time.Hour),
		SessionJWT:     f.mintSessionJWTLocked(sourceSess.MemberID, targetOrgID, time.Hour),
		SessionToken:   "session_token_" + newSessionID,
	}
	targetOrg.Sessions[newSessionID] = newSess
	return newSess, nil
}

func (f *Fake) RevokeSession(_ context.Context, sessionID string) error {
	if err := f.before("RevokeSession", sessionID); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, org := range f.Orgs {
		if _, ok := org.Sessions[sessionID]; ok {
			delete(org.Sessions, sessionID)
			return nil
		}
	}
	// Revoking an already-absent session is not an error per Stytch semantics.
	return nil
}

func (f *Fake) RevokeMemberSessions(_ context.Context, orgID, memberID string) error {
	if err := f.before("RevokeMemberSessions", map[string]string{
		"orgID":    orgID,
		"memberID": memberID,
	}); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	org, ok := f.Orgs[orgID]
	if !ok {
		return &Error{Kind: KindNotFound, Op: "RevokeMemberSessions", Status: 404}
	}

	for id, sess := range org.Sessions {
		if sess.MemberID == memberID {
			delete(org.Sessions, id)
		}
	}
	return nil
}

// Compile-time assertion that *Fake implements Client.
var _ Client = (*Fake)(nil)

// Compile-time assertion that *Fake implements JWKSSource.
var _ JWKSSource = (*Fake)(nil)
