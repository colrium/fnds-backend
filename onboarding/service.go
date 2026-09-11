package onboarding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"backend/configs"
	"backend/identity"
	"backend/ratelimit"
	"backend/stytch"
	"backend/turnstile"
	"backend/users"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ServiceError represents a domain error with HTTP status and optional machine-readable code.
type ServiceError struct {
	Status  int
	Code    string
	Message string
	Field   string
	// SuggestedAction names the recovery the caller should offer, when one
	// exists (e.g. "use_different_email"). Empty means no specific recovery.
	SuggestedAction string
}

func (e *ServiceError) Error() string { return e.Message }

// InitiateInput carries the parameters for InitiateMagicLink.
type InitiateInput struct {
	Email          string
	TurnstileToken string
	IP             string
	RedirectURL    string
}

// InitiateResult is the response from InitiateMagicLink.
type InitiateResult struct {
	Stage string `json:"stage"` // always "verification_sent"
}

// Service drives the employer signup state machine.
type Service struct {
	adapter *stytch.Adapter
	rate    ratelimit.RateLimiter
	// turnstile and other deps will be added as tasks progress
}

// NewService constructs an onboarding Service.
func NewService(adapter *stytch.Adapter, rate ratelimit.RateLimiter) *Service {
	return &Service{
		adapter: adapter,
		rate:    rate,
	}
}

// InitiateMagicLink starts the employer signup flow by sending a magic-link email.
//
// Steps:
//  1. Normalize the email address
//  2. Validate email format (RFC 5322) — return 400 with zero Stytch calls on failure
//  3. Verify Turnstile token with action "employer_signup" — return 400 with zero Stytch calls on rejection
//  4. Call Stytch DiscoveryEmailSend
//  5. Return a fixed 200 {"stage":"verification_sent"} body regardless of whether the address exists (Req 25.4)
//
// The response is byte-identical whether or not a matching users document exists,
// so account existence is not disclosed to an unauthenticated caller.
func (s *Service) InitiateMagicLink(ctx context.Context, in InitiateInput) (InitiateResult, error) {
	// 1. Normalize the email address.
	normalized := identity.NormalizeEmail(in.Email)

	// 2. Validate email format (RFC 5322). Return 400 with zero Stytch calls on failure (Req 1.5).
	if _, err := mail.ParseAddress(normalized); err != nil {
		return InitiateResult{}, &ServiceError{
			Status:  400,
			Code:    "invalid_email",
			Message: fmt.Sprintf("invalid email address: %s", err.Error()),
			Field:   "email",
		}
	}

	// 3. Verify Turnstile token (Req 1.9, 1.10).
	// A rejection is 400 with zero Stytch calls.
	if err := turnstile.Verify(ctx, in.TurnstileToken, in.IP, "employer_signup"); err != nil {
		if errors.Is(err, turnstile.ErrInvalid) {
			return InitiateResult{}, &ServiceError{
				Status:  400,
				Code:    "turnstile_rejected",
				Message: "bot verification failed",
				Field:   "turnstile_token",
			}
		}
		// ErrUnavailable or unexpected errors — treat as a rejection to fail closed.
		return InitiateResult{}, &ServiceError{
			Status:  400,
			Code:    "turnstile_unavailable",
			Message: "bot verification unavailable",
			Field:   "turnstile_token",
		}
	}

	// 4. Call Stytch DiscoveryEmailSend.
	err := s.adapter.Do(ctx, "DiscoveryEmailSend", false, func(attemptCtx context.Context) error {
		return s.adapter.Client().DiscoveryEmailSend(attemptCtx, stytch.DiscoveryEmailSendInput{
			Email:       normalized,
			RedirectURL: in.RedirectURL,
		})
	})
	if err != nil {
		return InitiateResult{}, fmt.Errorf("initiate magic link: %w", err)
	}

	// 5. Return a fixed 200 body — identical whether or not the address exists (Req 25.4).
	return InitiateResult{Stage: "verification_sent"}, nil
}

// OAuthStartURL returns the redirect URL to start the Stytch OAuth discovery flow
// for the given provider. Only "google" is supported at launch (Req 1.8).
// Returns an error for unsupported providers.
func (s *Service) OAuthStartURL(ctx context.Context, provider, redirectURL string) (string, error) {
	if provider != "google" {
		return "", &ServiceError{
			Status:  400,
			Code:    "unsupported_provider",
			Message: fmt.Sprintf("unsupported OAuth provider: %q; supported: google", provider),
			Field:   "provider",
		}
	}

	url, err := s.adapter.Client().OAuthDiscoveryStartURL(provider, redirectURL)
	if err != nil {
		return "", fmt.Errorf("oauth start url: %w", err)
	}

	return url, nil
}

// AuthenticateInput carries the parameters for Authenticate.
type AuthenticateInput struct {
	DiscoveryToken string // the magic-link or OAuth token from Stytch
	TokenType      string // "magic_link" or "oauth"
}

// AuthResult is the response from Authenticate.
type AuthResult struct {
	Stage          string              `json:"stage"`
	OnboardingID   string              `json:"onboarding_id,omitempty"`
	LinkCandidates []LinkCandidateView `json:"link_candidates,omitempty"`

	// Internal fields the HTTP layer uses to establish a real workspace
	// session when Stage is "complete" — never serialized to the client.
	// workspaces can't be imported here (it imports onboarding), so the
	// session exchange itself happens in workspaces/routes.go.
	UserID                   string `json:"-"`
	WorkspaceID              string `json:"-"`
	StytchOrganizationID     string `json:"-"`
	IntermediateSessionToken string `json:"-"`
	// Email is the authenticated, normalized address. ResolvePostAuth needs it
	// to find pending invitations addressed to this person.
	Email string `json:"-"`
}

// LinkCandidateView is the masked public representation of a link candidate.
type LinkCandidateView struct {
	ID        string `json:"id"`
	Email     string `json:"email"` // masked
	Phone     string `json:"phone"` // masked
	UserType  string `json:"user_type"`
	CreatedAt string `json:"created_at"`
}

// Authenticate redeems a Stytch discovery token and determines the next onboarding stage.
//
// Steps:
//  1. Call Stytch DiscoveryAuthenticate (or OAuthDiscoveryAuthenticate based on TokenType)
//  2. Treat email as verified only when Stytch reports it verified; unverified → 403 (Req 1.6, 1.7)
//  3. Upsert onboarding_states on {stytch_member_id} so re-authentication resumes (Req 3.6)
//  4. Search candidates ONLY AFTER verified authentication via GetUsersByNormalizedEmail (Req 25.5)
//  5. With candidates → set stage awaiting_link_decision, return masked candidates (Req 4.2)
//  6. With zero candidates → insert users document, set stage awaiting_workspace_name (Req 1.3, 1.11-1.13)
func (s *Service) Authenticate(ctx context.Context, in AuthenticateInput) (AuthResult, error) {
	// 1. Call the appropriate Stytch discovery method based on TokenType.
	var result stytch.DiscoveryResult
	var authErr error

	switch in.TokenType {
	case "magic_link":
		authErr = s.adapter.Do(ctx, "DiscoveryAuthenticate", false, func(attemptCtx context.Context) error {
			var err error
			result, err = s.adapter.Client().DiscoveryAuthenticate(attemptCtx, in.DiscoveryToken)
			return err
		})
	case "oauth":
		authErr = s.adapter.Do(ctx, "OAuthDiscoveryAuthenticate", false, func(attemptCtx context.Context) error {
			var err error
			result, err = s.adapter.Client().OAuthDiscoveryAuthenticate(attemptCtx, in.DiscoveryToken)
			return err
		})
	default:
		return AuthResult{}, &ServiceError{
			Status:  400,
			Code:    "invalid_token_type",
			Message: fmt.Sprintf("unsupported token type: %q; supported: magic_link, oauth", in.TokenType),
			Field:   "token_type",
		}
	}

	if authErr != nil {
		return AuthResult{}, fmt.Errorf("authenticate: %w", authErr)
	}

	// 2. Treat email as verified only when Stytch reports it verified (Req 1.6, 1.7).
	// Unverified → 403 with zero workspaces, zero memberships, zero writes.
	if !result.EmailVerified {
		return AuthResult{}, &ServiceError{
			Status:  403,
			Code:    "email_verification_required",
			Message: "email verification is required",
		}
	}

	// Normalize the email for all subsequent operations.
	normalizedEmail := identity.NormalizeEmail(result.Email)

	// Determine the signup method for recording (Req 1.16).
	signupMethod := in.TokenType

	// 3. Upsert into onboarding_states using stytch_member_id as the key (Req 3.6).
	// Re-authentication resumes rather than creating a second record.
	coll := configs.MI.DB.Collection("onboarding_states")
	onboardingID := uuid.New().String()
	now := time.Now()

	// With neither key the filter degenerates to {"email": ""}, which can match
	// an unrelated document and hand this caller someone else's onboarding
	// state. 502 rather than 500: the fault is upstream, not in our own logic,
	// and the condition is plausibly transient so retrying is reasonable.
	if result.MemberID == "" && normalizedEmail == "" {
		slog.ErrorContext(ctx, "authenticate: stytch returned neither member ID nor email",
			"token_type", in.TokenType,
			"email_verified", result.EmailVerified,
		)
		return AuthResult{}, &ServiceError{
			Status:  502,
			Code:    "missing_identity",
			Message: "the sign-in provider returned an incomplete identity; please try again",
		}
	}

	filter := authenticationStateFilter(result.MemberID, normalizedEmail)
	stateFields := bson.M{
		"email":                             normalizedEmail,
		"email_verified":                    true,
		"stytch_intermediate_session_token": result.IntermediateSessionToken,
		"updated_at":                        now,
	}
	if result.MemberID != "" {
		stateFields["stytch_member_id"] = result.MemberID
	}
	update := bson.M{
		"$setOnInsert": bson.M{
			"_id":             onboardingID,
			"stage":           StageAwaitingVerification,
			"signup_method":   signupMethod,
			"idempotency_key": "onboarding:" + onboardingID,
			"created_at":      now,
		},
		"$set": stateFields,
	}

	// Also set first/last name from Stytch if reported (Req 1.15).
	if result.Name != "" {
		update["$set"].(bson.M)["first_name"] = result.Name
	}

	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var state OnboardingState
	err := coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&state)
	if err != nil {
		return AuthResult{}, fmt.Errorf("authenticate: upsert onboarding_states: %w", err)
	}

	// If the state already has a stage beyond awaiting_verification, resume at that stage (Req 3.6).
	if state.Stage != StageAwaitingVerification && state.Stage != "" {
		// Resuming an existing flow — return current stage. For a returning,
		// fully-onboarded member (StageComplete), also surface the raw
		// identifiers the HTTP layer needs to exchange this call's fresh
		// IntermediateSessionToken for a real workspace session — the
		// onboarding_ticket alone only carries stage/state, not a session.
		authResult := AuthResult{
			Stage:        string(state.Stage),
			OnboardingID: state.ID,
		}
		if state.Stage == StageComplete {
			orgID, resolveErr := completedWorkspaceOrganizationID(ctx, coll, &state)
			if resolveErr != nil {
				return AuthResult{}, resolveErr
			}
			authResult.UserID = state.UserID
			authResult.WorkspaceID = state.WorkspaceID
			authResult.StytchOrganizationID = orgID
			authResult.IntermediateSessionToken = result.IntermediateSessionToken
			authResult.Email = normalizedEmail
		}
		return authResult, nil
	}

	// 4. Search candidates ONLY AFTER verified authentication via GetUsersByNormalizedEmail (Req 25.5).
	allUsers, err := users.GetUsersByNormalizedEmail(ctx, normalizedEmail)
	if err != nil {
		return AuthResult{}, fmt.Errorf("authenticate: search candidates: %w", err)
	}

	// 5. Filter candidates:
	//    - Exclude those already linked (have account_links) (Req 4.4)
	//    - Exclude non-customer types: artisan, assessor, admin (Req 4.9)
	//    - Exclude suspended (Req 4.10)
	var candidates []users.User
	accountLinksColl := configs.MI.DB.Collection("account_links")

	// Tracks why every candidate was withheld, so a caller left with zero
	// candidates gets the specific reason (Req 4.9, 4.10) instead of falling
	// through to the create-a-user path and colliding on the unique email
	// index — email is globally unique across every user_type.
	var withheldNonEmployer, withheldSuspended bool

	// An already-linked users document is not a candidate to link *to* — it is
	// the returning Employer themselves (Req 5.12). Captured so they can be
	// signed in below instead of falling through to the create path, where the
	// globally unique email index would reject the insert.
	var linkedUser *users.User
	var linkedOrgID string

	for i := range allUsers {
		u := allUsers[i]

		// Exclude non-customer types (Req 4.9).
		if u.UserType == "artisan" || u.UserType == "assessor" || u.UserType == "admin" {
			withheldNonEmployer = true
			continue
		}

		// Exclude suspended users (Req 4.10).
		if u.Status == users.StatusSuspended {
			withheldSuspended = true
			continue
		}

		// Exclude users that already have an account_link.
		var link identity.AccountLink
		linkErr := accountLinksColl.FindOne(ctx, bson.M{"user_id": u.ID}).Decode(&link)
		if linkErr == nil {
			// Has an account_link — not a link target, but a sign-in target.
			// First match wins: at most one link exists per users document, so
			// with legacy case-variant duplicates any linked one identifies
			// the same person.
			if linkedUser == nil {
				linkedUser = &u
				linkedOrgID = link.StytchOrganizationID
			}
			continue
		}
		if !errors.Is(linkErr, mongo.ErrNoDocuments) {
			return AuthResult{}, fmt.Errorf("authenticate: check account_link for user %s: %w", u.ID, linkErr)
		}

		candidates = append(candidates, u)
	}

	// 6. With candidates → set stage awaiting_link_decision, return masked candidates (Req 4.2).
	if len(candidates) > 0 {
		candidateIDs := make([]string, len(candidates))
		views := make([]LinkCandidateView, len(candidates))
		for i, c := range candidates {
			candidateIDs[i] = c.ID
			views[i] = LinkCandidateView{
				ID:        c.ID,
				Email:     identity.MaskEmail(c.Email),
				Phone:     identity.MaskPhone(c.Phone),
				UserType:  c.UserType,
				CreatedAt: c.CreatedAt.Format(time.RFC3339),
			}
		}

		// Transition stage to awaiting_link_decision.
		_, err = coll.UpdateOne(ctx,
			bson.M{"_id": state.ID, "stage": StageAwaitingVerification},
			bson.M{"$set": bson.M{
				"stage":              StageAwaitingLinkDecision,
				"link_candidate_ids": candidateIDs,
				"updated_at":         time.Now(),
			}},
		)
		if err != nil {
			return AuthResult{}, fmt.Errorf("authenticate: update stage to awaiting_link_decision: %w", err)
		}

		return AuthResult{
			Stage:          string(StageAwaitingLinkDecision),
			OnboardingID:   state.ID,
			LinkCandidates: views,
		}, nil
	}

	// An existing Account_Link means this person already completed onboarding:
	// sign them in and create zero additional users documents (Req 5.12).
	// Checked before the withheld cases so a returning Employer always signs
	// in, even when a legacy case-variant duplicate of their address also
	// exists as an artisan/assessor/admin or suspended record.
	//
	// This is the path a returning Employer takes whenever their
	// onboarding_states document no longer resumes them at StageComplete —
	// previously they fell through to the create path and hit a raw duplicate
	// key on the unique email index, which is why signing in could look like a
	// failed attempt to register an account that already existed.
	//
	// The stale onboarding_states document is deliberately left alone: it only
	// drives in-progress onboarding, and this person has none left to do.
	if linkedUser != nil {
		return AuthResult{
			Stage:                    string(StageComplete),
			OnboardingID:             state.ID,
			UserID:                   linkedUser.ID,
			Email:                    normalizedEmail,
			StytchOrganizationID:     linkedOrgID,
			IntermediateSessionToken: result.IntermediateSessionToken,
		}, nil
	}

	// Zero candidates, but at least one match was withheld: report why rather
	// than attempting an insert that the unique email index would reject.
	// Suspended is checked first — it is the more actionable of the two and
	// directs the caller to support rather than to a different address.
	if withheldSuspended {
		return AuthResult{}, &ServiceError{
			Status:          409,
			Code:            "account_suspended",
			Message:         "an account for this email is suspended; please contact support",
			Field:           "email",
			SuggestedAction: "contact_support",
		}
	}
	if withheldNonEmployer {
		return AuthResult{}, &ServiceError{
			Status:          409,
			Code:            "email_belongs_to_non_employer",
			Message:         "this email is already registered as an artisan, assessor, or admin account; use a different email for an employer account or contact support",
			Field:           "email",
			SuggestedAction: "use_different_email",
		}
	}

	// 7. With zero candidates → insert users document, set stage awaiting_workspace_name (Req 1.3, 1.11-1.13).
	newUser := &users.User{
		Email:    normalizedEmail,
		UserType: "customer",
		Status:   users.StatusActive,
		IsActive: true,
		Password: []byte{}, // zero password bytes (Req 1.11)
		// Phone field is OMITTED — the omitempty bson tag on User.Phone ensures
		// the field is absent from the inserted document (Req 1.12).
	}

	if err := users.CreateNewUser(newUser); err != nil {
		// Email is globally unique across all user types (see
		// users.EnsureIndexes). The candidate search above only offers linking
		// for customer-type accounts (Req 4.9), so an email already registered
		// as artisan/assessor/admin reaches here with zero candidates and
		// collides on insert. That is a real, expected conflict — not a
		// transient provider failure — so it gets its own status/code rather
		// than falling into the generic "authenticate: create user" wrap that
		// routes.go maps to a misleading 503 "auth_provider_unavailable".
		if mongo.IsDuplicateKeyError(err) {
			return AuthResult{}, &ServiceError{
				Status:  409,
				Code:    "email_already_registered",
				Message: "this email is already registered under a different account type",
				Field:   "email",
			}
		}
		return AuthResult{}, fmt.Errorf("authenticate: create user: %w", err)
	}

	// Transition stage to awaiting_workspace_name and record the user_id.
	_, err = coll.UpdateOne(ctx,
		bson.M{"_id": state.ID, "stage": StageAwaitingVerification},
		bson.M{"$set": bson.M{
			"stage":      StageAwaitingWorkspaceName,
			"user_id":    newUser.ID,
			"updated_at": time.Now(),
		}},
	)
	if err != nil {
		return AuthResult{}, fmt.Errorf("authenticate: update stage to awaiting_workspace_name: %w", err)
	}

	return AuthResult{
		Stage:        string(StageAwaitingWorkspaceName),
		OnboardingID: state.ID,
	}, nil
}

// authenticationStateFilter selects the onboarding_states document to resume.
//
// The first authentication of a new employer creates the document before any
// stytch_member_id is known, so it is keyed only by email. A later
// authentication that does carry a MemberID must still find that document —
// matching on stytch_member_id alone would miss it and the upsert would create
// a second, competing onboarding record, breaking the resumability Req 3.6
// promises. Hence the $or when both keys are available.
func authenticationStateFilter(memberID, normalizedEmail string) bson.M {
	switch {
	case memberID != "" && normalizedEmail != "":
		return bson.M{"$or": []bson.M{
			{"stytch_member_id": memberID},
			{"email": normalizedEmail},
		}}
	case memberID != "":
		return bson.M{"stytch_member_id": memberID}
	default:
		return bson.M{"email": normalizedEmail}
	}
}

// LinkDecision represents the employer's choice in the awaiting_link_decision stage.
type LinkDecision struct {
	CandidateID    string // link to this candidate (mutually exclusive with CreateSeparate)
	CreateSeparate bool   // create a separate account instead
	Email          string // required with CreateSeparate: must differ from the Stytch-verified email (Req 4.6)
}

// LinkResult is the response from DecideLink.
type LinkResult struct {
	Stage     string `json:"stage"`
	Challenge string `json:"challenge,omitempty"` // "stytch_email_match" or "phone_otp"
}

// SubmitName collects a first and last name before the state reaches complete,
// pre-filled from the Stytch-reported name when present (Req 1.15).
//
// This may be called in any stage before complete. It sets the first_name and
// last_name fields on the onboarding_states document.
func (s *Service) SubmitName(ctx context.Context, stateID, first, last string) error {
	if stateID == "" {
		return &ServiceError{
			Status:  400,
			Code:    "invalid_state_id",
			Message: "state ID is required",
			Field:   "state_id",
		}
	}

	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)

	if first == "" {
		return &ServiceError{
			Status:  400,
			Code:    "name_required",
			Message: "first name is required",
			Field:   "first_name",
		}
	}
	if last == "" {
		return &ServiceError{
			Status:  400,
			Code:    "name_required",
			Message: "last name is required",
			Field:   "last_name",
		}
	}

	coll := configs.MI.DB.Collection("onboarding_states")

	// Update the name on the onboarding_states document. We allow this update
	// in any stage that is not complete, because the name can be submitted or
	// corrected before the final workspace provisioning.
	filter := bson.M{
		"_id":   stateID,
		"stage": bson.M{"$ne": string(StageComplete)},
	}
	update := bson.M{
		"$set": bson.M{
			"first_name": first,
			"last_name":  last,
			"updated_at": time.Now(),
		},
	}

	result, err := coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("submit name: update onboarding_states: %w", err)
	}
	if result.MatchedCount == 0 {
		return &ServiceError{
			Status:  409,
			Code:    "invalid_stage",
			Message: "onboarding state not found or already complete",
		}
	}

	return nil
}

// DecideLink processes the employer's linking decision during onboarding.
//
// Accepts exactly two decisions:
//  1. Link to a named Link_Candidate
//  2. Create a separate new Employer account
//
// Exclusion rules applied:
//   - Already linked to a different member → 409 email_already_linked (Req 4.4)
//   - user_type in {artisan, assessor, admin} → 409 email_belongs_to_non_employer (Req 4.9)
//   - Status suspended → 409 link_candidate_suspended (Req 4.10)
//   - Create separate but same email → require different email (Req 4.6)
//
// Requirements: 1.15, 4.2, 4.4, 4.5, 4.6, 4.8, 4.9, 4.10
func (s *Service) DecideLink(ctx context.Context, stateID string, d LinkDecision) (LinkResult, error) {
	if stateID == "" {
		return LinkResult{}, &ServiceError{
			Status:  400,
			Code:    "invalid_state_id",
			Message: "state ID is required",
			Field:   "state_id",
		}
	}

	// Exactly one of CandidateID or CreateSeparate must be set.
	if d.CandidateID != "" && d.CreateSeparate {
		return LinkResult{}, &ServiceError{
			Status:  400,
			Code:    "invalid_decision",
			Message: "candidate_id and create_separate are mutually exclusive",
		}
	}
	if d.CandidateID == "" && !d.CreateSeparate {
		return LinkResult{}, &ServiceError{
			Status:  400,
			Code:    "invalid_decision",
			Message: "one of candidate_id or create_separate must be specified",
		}
	}

	// 1. Load the onboarding state by ID, verify stage is awaiting_link_decision.
	coll := configs.MI.DB.Collection("onboarding_states")
	var state OnboardingState
	err := coll.FindOne(ctx, bson.M{"_id": stateID}).Decode(&state)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return LinkResult{}, &ServiceError{
				Status:  404,
				Code:    "state_not_found",
				Message: "onboarding state not found",
			}
		}
		return LinkResult{}, fmt.Errorf("decide link: load state: %w", err)
	}

	if state.Stage != StageAwaitingLinkDecision {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "invalid_stage",
			Message: fmt.Sprintf("expected stage %q, got %q", StageAwaitingLinkDecision, state.Stage),
		}
	}

	// 2. If CreateSeparate: advance stage to awaiting_workspace_name.
	if d.CreateSeparate {
		// Req 4.6: creating a separate account requires a DIFFERENT email than
		// the Stytch-verified one already tied to candidates — reusing it
		// would hit the users collection's unique email index and additionally
		// contradicts the requirement's intent.
		normalizedNew := identity.NormalizeEmail(d.Email)
		if normalizedNew == "" {
			return LinkResult{}, &ServiceError{
				Status:  400,
				Code:    "email_required",
				Message: "email is required when creating a separate account",
				Field:   "email",
			}
		}
		if normalizedNew == identity.NormalizeEmail(state.Email) {
			return LinkResult{}, &ServiceError{
				Status:  400,
				Code:    "email_must_differ",
				Message: "the separate account's email must differ from the Stytch-verified email",
				Field:   "email",
			}
		}
		existing, err := users.GetUsersByNormalizedEmail(ctx, normalizedNew)
		if err != nil {
			return LinkResult{}, fmt.Errorf("decide link: check email collision: %w", err)
		}
		if len(existing) > 0 {
			return LinkResult{}, &ServiceError{
				Status:  409,
				Code:    "email_in_use",
				Message: "an account with this email already exists",
				Field:   "email",
			}
		}
		return s.decideLinkCreateSeparate(ctx, &state, d.Email)
	}

	// 3. If CandidateID != "": process the link decision.
	return s.decideLinkToCandidate(ctx, &state, d.CandidateID)
}

// decideLinkCreateSeparate handles the "create a separate account" path.
// email must already be validated as distinct from the Stytch-verified email
// and free of collisions (Req 4.6). The user document is created BEFORE the
// stage transition: creating it after (as a prior version of this function
// did) leaves a phantom awaiting_workspace_name stage with no user_id if
// creation fails partway, from which onboarding can neither proceed nor go
// back. Advances stage to awaiting_workspace_name (Req 4.5).
func (s *Service) decideLinkCreateSeparate(ctx context.Context, state *OnboardingState, email string) (LinkResult, error) {
	newUser := &users.User{
		Email:    email,
		UserType: "customer",
		Status:   users.StatusActive,
		IsActive: true,
		Password: []byte{}, // zero password bytes (Req 1.11)
		// Phone field is OMITTED (Req 1.12).
	}
	if state.FirstName != "" {
		newUser.FirstName = state.FirstName
	}
	if state.LastName != "" {
		newUser.LastName = state.LastName
	}

	if err := users.CreateNewUser(newUser); err != nil {
		return LinkResult{}, fmt.Errorf("decide link create separate: create user: %w", err)
	}

	// Advance stage from awaiting_link_decision to awaiting_workspace_name
	// and record user_id in the same write.
	coll := configs.MI.DB.Collection("onboarding_states")
	filter := bson.M{
		"_id":   state.ID,
		"stage": string(StageAwaitingLinkDecision),
	}
	update := bson.M{
		"$set": bson.M{
			"stage":      string(StageAwaitingWorkspaceName),
			"user_id":    newUser.ID,
			"updated_at": time.Now(),
		},
	}

	result, err := coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return LinkResult{}, fmt.Errorf("decide link create separate: update stage: %w", err)
	}
	if result.MatchedCount == 0 {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "stage_conflict",
			Message: "concurrent stage transition detected",
		}
	}

	return LinkResult{Stage: string(StageAwaitingWorkspaceName)}, nil
}

// decideLinkToCandidate handles the "link to an existing user" path.
// Applies exclusion rules and determines the challenge method.
func (s *Service) decideLinkToCandidate(ctx context.Context, state *OnboardingState, candidateID string) (LinkResult, error) {
	// Validate that the candidateID is one of the recorded link_candidate_ids.
	found := false
	for _, id := range state.LinkCandidateIDs {
		if id == candidateID {
			found = true
			break
		}
	}
	if !found {
		return LinkResult{}, &ServiceError{
			Status:  400,
			Code:    "invalid_candidate",
			Message: "candidate_id is not in the link candidate set",
			Field:   "candidate_id",
		}
	}

	// Load the candidate user from the users collection.
	usersColl := configs.MI.DB.Collection("users")
	var candidate users.User
	err := usersColl.FindOne(ctx, bson.M{"_id": candidateID}).Decode(&candidate)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return LinkResult{}, &ServiceError{
				Status:  404,
				Code:    "candidate_not_found",
				Message: "link candidate user not found",
			}
		}
		return LinkResult{}, fmt.Errorf("decide link to candidate: load user: %w", err)
	}

	// Apply exclusion rules:

	// Rule 1: Already linked to a different member → 409 email_already_linked (Req 4.4).
	// This is a UX check only: a concurrent request can create the link
	// between this read and the actual link write. The real guarantee is the
	// unique index on account_links.user_id (identity.EnsureIndexes,
	// uniq_account_link_user) — the write path must handle its duplicate-key
	// error rather than relying on this check alone.
	accountLinksColl := configs.MI.DB.Collection("account_links")
	var existingLink identity.AccountLink
	linkErr := accountLinksColl.FindOne(ctx, bson.M{"user_id": candidateID}).Decode(&existingLink)
	if linkErr == nil {
		// Already has an account_link to a different member.
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "email_already_linked",
			Message: "this account is already linked to a different member; please sign in instead",
		}
	}
	if !errors.Is(linkErr, mongo.ErrNoDocuments) {
		return LinkResult{}, fmt.Errorf("decide link to candidate: check account_link: %w", linkErr)
	}

	// Rule 2: user_type in {artisan, assessor, admin} → 409 email_belongs_to_non_employer (Req 4.9).
	if candidate.UserType == "artisan" || candidate.UserType == "assessor" || candidate.UserType == "admin" {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "email_belongs_to_non_employer",
			Message: "this email belongs to a non-employer account; please use a different email or contact support",
		}
	}

	// Rule 3: Status suspended → 409 link_candidate_suspended (Req 4.10).
	if candidate.Status == users.StatusSuspended {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "link_candidate_suspended",
			Message: "this account is suspended; please contact support",
		}
	}

	// Determine the challenge method using identity.SelectChallenge (Req 5.1-5.3).
	challengeMethod, err := identity.SelectChallenge(candidate.Email, candidate.Phone, state.Email)
	if err != nil {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "link_not_possible",
			Message: err.Error(),
		}
	}

	coll := configs.MI.DB.Collection("onboarding_states")

	// If stytch_email_match: the Stytch verification is sufficient proof.
	// Advance directly to awaiting_workspace_name (Req 5.2).
	if challengeMethod == identity.ChallengeStytchEmailMatch {
		filter := bson.M{
			"_id":   state.ID,
			"stage": string(StageAwaitingLinkDecision),
		}
		update := bson.M{
			"$set": bson.M{
				"stage":                      string(StageAwaitingWorkspaceName),
				"selected_link_candidate_id": candidateID,
				"user_id":                    candidateID,
				"challenge": bson.M{
					"method":     string(identity.ChallengeStytchEmailMatch),
					"issued_at":  time.Now(),
					"expires_at": time.Now().Add(15 * time.Minute),
					"attempts":   0,
				},
				"updated_at": time.Now(),
			},
		}

		result, err := coll.UpdateOne(ctx, filter, update)
		if err != nil {
			return LinkResult{}, fmt.Errorf("decide link to candidate: update state (email match): %w", err)
		}
		if result.MatchedCount == 0 {
			return LinkResult{}, &ServiceError{
				Status:  409,
				Code:    "stage_conflict",
				Message: "concurrent stage transition detected",
			}
		}

		return LinkResult{
			Stage:     string(StageAwaitingWorkspaceName),
			Challenge: string(identity.ChallengeStytchEmailMatch),
		}, nil
	}

	// If phone_otp: record the challenge on the state but stay in awaiting_link_decision
	// because the OTP must be verified before advancing (Req 5.3).
	filter := bson.M{
		"_id":   state.ID,
		"stage": string(StageAwaitingLinkDecision),
	}
	update := bson.M{
		"$set": bson.M{
			"selected_link_candidate_id": candidateID,
			"challenge": bson.M{
				"method":     string(identity.ChallengePhoneOTP),
				"issued_at":  time.Now(),
				"expires_at": time.Now().Add(15 * time.Minute),
				"attempts":   0,
			},
			"updated_at": time.Now(),
		},
	}

	result, err := coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return LinkResult{}, fmt.Errorf("decide link to candidate: update state (phone otp): %w", err)
	}
	if result.MatchedCount == 0 {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "stage_conflict",
			Message: "concurrent stage transition detected",
		}
	}

	return LinkResult{
		Stage:     string(StageAwaitingLinkDecision),
		Challenge: string(identity.ChallengePhoneOTP),
	}, nil
}

// StateByID loads an onboarding_states document by its ID. Used by the HTTP
// layer to resolve an Onboarding_Ticket's state ID into the full record
// before calling into WorkspaceService, which cannot import this package
// (see workspaces/routes.go for why the cross-package orchestration lives
// there instead of here).
func (s *Service) StateByID(ctx context.Context, stateID string) (*OnboardingState, error) {
	if stateID == "" {
		return nil, &ServiceError{
			Status:  400,
			Code:    "invalid_state_id",
			Message: "state ID is required",
			Field:   "state_id",
		}
	}

	coll := configs.MI.DB.Collection("onboarding_states")
	var state OnboardingState
	err := coll.FindOne(ctx, bson.M{"_id": stateID}).Decode(&state)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, &ServiceError{
				Status:  404,
				Code:    "state_not_found",
				Message: "onboarding state not found",
			}
		}
		return nil, fmt.Errorf("state by id: %w", err)
	}
	return &state, nil
}

// CompleteOnboarding advances the onboarding_states document to the terminal
// complete stage and records the provisioned workspace ID. The update is
// conditioned on the document currently being in awaiting_workspace_name so
// a retried or concurrent completion call cannot double-apply.
func (s *Service) CompleteOnboarding(ctx context.Context, stateID, workspaceID string) error {
	coll := configs.MI.DB.Collection("onboarding_states")
	now := time.Now()

	orgID, err := workspaceOrganizationID(ctx, workspaceID)
	if err != nil {
		return err
	}

	filter := bson.M{"_id": stateID, "stage": string(StageAwaitingWorkspaceName)}
	update := bson.M{
		"$set": bson.M{
			"stage":                  string(StageComplete),
			"workspace_id":           workspaceID,
			"stytch_organization_id": orgID,
			"completed_at":           now,
			"updated_at":             now,
		},
	}

	result, err := coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("complete onboarding: %w", err)
	}
	if result.MatchedCount == 0 {
		return &ServiceError{
			Status:  409,
			Code:    "invalid_stage",
			Message: "onboarding state is not awaiting workspace provisioning",
		}
	}

	return nil
}

func completedWorkspaceOrganizationID(ctx context.Context, states *mongo.Collection, state *OnboardingState) (string, error) {
	orgID, err := workspaceOrganizationID(ctx, state.WorkspaceID)
	if err != nil {
		return "", err
	}

	if state.StytchOrganizationID != "" && state.StytchOrganizationID != orgID {
		return "", &ServiceError{
			Status:          503,
			Code:            "auth_data_inconsistent",
			Message:         "employer sign-in is temporarily unavailable; please contact support",
			SuggestedAction: "contact_support",
		}
	}

	if state.StytchOrganizationID == "" {
		if _, err := states.UpdateOne(ctx,
			bson.M{"_id": state.ID, "stage": string(StageComplete)},
			bson.M{"$set": bson.M{
				"stytch_organization_id": orgID,
				"updated_at":             time.Now(),
			}},
		); err != nil {
			return "", fmt.Errorf("authenticate: backfill onboarding organization: %w", err)
		}
	}

	return orgID, nil
}

func workspaceOrganizationID(ctx context.Context, workspaceID string) (string, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return "", &ServiceError{
			Status:          503,
			Code:            "workspace_link_missing",
			Message:         "employer sign-in is temporarily unavailable; please contact support",
			SuggestedAction: "contact_support",
		}
	}

	var workspace struct {
		StytchOrganizationID string `bson:"stytch_organization_id"`
	}
	err := configs.MI.DB.Collection("workspaces").FindOne(
		ctx,
		bson.M{"_id": workspaceID},
		options.FindOne().SetProjection(bson.M{"stytch_organization_id": 1}),
	).Decode(&workspace)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", &ServiceError{
				Status:          503,
				Code:            "workspace_link_missing",
				Message:         "employer sign-in is temporarily unavailable; please contact support",
				SuggestedAction: "contact_support",
			}
		}
		return "", fmt.Errorf("load workspace organization: %w", err)
	}
	if strings.TrimSpace(workspace.StytchOrganizationID) == "" {
		return "", &ServiceError{
			Status:          503,
			Code:            "workspace_link_missing",
			Message:         "employer sign-in is temporarily unavailable; please contact support",
			SuggestedAction: "contact_support",
		}
	}
	return workspace.StytchOrganizationID, nil
}

// IssueChallengeResult is the response from IssueLinkChallenge.
type IssueChallengeResult struct {
	Stage       string `json:"stage"`
	Challenge   string `json:"challenge,omitempty"`
	MaskedPhone string `json:"masked_phone,omitempty"`
}

// challengeValidityWindow bounds how long a re-issued phone_otp
// Link_Challenge stays valid (Req 5.4).
const challengeValidityWindow = 15 * time.Minute

// IssueLinkChallenge sends the phone OTP for an in-flight phone_otp
// Link_Challenge that DecideLink recorded on the state. A stytch_email_match
// challenge needs no separate issuance step — DecideLink already advanced
// that stage directly — so calling this for a state without a pending
// phone_otp challenge is a stage conflict, not a retry target.
//
// Requirements: 5.3, 5.4, 5.5
func (s *Service) IssueLinkChallenge(ctx context.Context, stateID string) (IssueChallengeResult, error) {
	state, err := s.StateByID(ctx, stateID)
	if err != nil {
		return IssueChallengeResult{}, err
	}

	if state.Stage != StageAwaitingLinkDecision || state.Challenge == nil ||
		state.Challenge.Method != ChallengePhoneOTP || state.SelectedCandidateID == "" {
		return IssueChallengeResult{}, &ServiceError{
			Status:  409,
			Code:    "invalid_stage",
			Message: "no phone verification challenge is pending for this onboarding state",
		}
	}

	// 5 submissions per candidate per hour (Req 5.5).
	decision := s.rate.Allow(ctx, ratelimit.LimitLinkChallenge, state.SelectedCandidateID)
	if !decision.Allowed {
		return IssueChallengeResult{}, &ServiceError{
			Status:  429,
			Code:    "link_challenge_rate_limited",
			Message: "too many verification attempts; try again later",
		}
	}

	candidate, err := users.GetUserByID(ctx, state.SelectedCandidateID)
	if err != nil {
		return IssueChallengeResult{}, fmt.Errorf("issue link challenge: load candidate: %w", err)
	}
	if candidate == nil || candidate.Phone == "" {
		return IssueChallengeResult{}, &ServiceError{
			Status:  409,
			Code:    "link_not_possible",
			Message: "the linked account has no phone number on file",
		}
	}

	users.IssuePhoneOTP(candidate.ID, candidate.Phone)

	now := time.Now()
	coll := configs.MI.DB.Collection("onboarding_states")
	_, err = coll.UpdateOne(ctx,
		bson.M{"_id": state.ID, "stage": string(StageAwaitingLinkDecision)},
		bson.M{"$set": bson.M{
			"challenge.issued_at":  now,
			"challenge.expires_at": now.Add(challengeValidityWindow),
			"challenge.attempts":   0,
			"updated_at":           now,
		}},
	)
	if err != nil {
		return IssueChallengeResult{}, fmt.Errorf("issue link challenge: refresh challenge: %w", err)
	}

	return IssueChallengeResult{
		Stage:       string(StageAwaitingLinkDecision),
		Challenge:   string(ChallengePhoneOTP),
		MaskedPhone: identity.MaskPhone(candidate.Phone),
	}, nil
}

// VerifyLinkChallenge checks a submitted phone OTP against the pending
// phone_otp Link_Challenge and, on success, advances the state to
// awaiting_workspace_name and records the candidate as the onboarding user.
//
// An incorrect or expired code makes zero Account_Links and increments the
// challenge's attempt count (Req 5.6). The OTP's own correctness is decided
// by users.VerifyOTP, which also marks the candidate's phone verified and
// active on success (Req 4.12).
//
// Requirements: 5.4, 5.6, 5.7
func (s *Service) VerifyLinkChallenge(ctx context.Context, stateID, code string) (LinkResult, error) {
	state, err := s.StateByID(ctx, stateID)
	if err != nil {
		return LinkResult{}, err
	}

	if state.Stage != StageAwaitingLinkDecision || state.Challenge == nil ||
		state.Challenge.Method != ChallengePhoneOTP || state.SelectedCandidateID == "" {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "invalid_stage",
			Message: "no phone verification challenge is pending for this onboarding state",
		}
	}

	coll := configs.MI.DB.Collection("onboarding_states")

	if time.Now().After(state.Challenge.ExpiresAt) {
		return LinkResult{}, &ServiceError{
			Status:  401,
			Code:    "challenge_expired",
			Message: "the verification code has expired; request a new one",
		}
	}

	if verifyErr := users.VerifyOTP(ctx, state.SelectedCandidateID, code); verifyErr != nil {
		_, incErr := coll.UpdateOne(ctx,
			bson.M{"_id": state.ID},
			bson.M{
				"$inc": bson.M{"challenge.attempts": 1},
				"$set": bson.M{"updated_at": time.Now()},
			},
		)
		if incErr != nil {
			return LinkResult{}, fmt.Errorf("verify link challenge: record failed attempt: %w", incErr)
		}
		return LinkResult{}, &ServiceError{
			Status:  401,
			Code:    "challenge_incorrect",
			Message: "incorrect verification code",
		}
	}

	now := time.Now()
	filter := bson.M{"_id": state.ID, "stage": string(StageAwaitingLinkDecision)}
	update := bson.M{
		"$set": bson.M{
			"stage":      string(StageAwaitingWorkspaceName),
			"user_id":    state.SelectedCandidateID,
			"updated_at": now,
		},
	}

	result, err := coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return LinkResult{}, fmt.Errorf("verify link challenge: advance stage: %w", err)
	}
	if result.MatchedCount == 0 {
		return LinkResult{}, &ServiceError{
			Status:  409,
			Code:    "stage_conflict",
			Message: "concurrent stage transition detected",
		}
	}

	return LinkResult{Stage: string(StageAwaitingWorkspaceName)}, nil
}
