package invitations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"backend/configs"
	"backend/identity"
	"backend/notifications"
	"backend/responses"
	"backend/stytch"
	"backend/users"
	"backend/workspaces"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// HandlersConfig carries the dependencies the HTTP handlers need beyond the
// Service itself: Adapter to talk to Stytch directly (discovery
// authentication, member creation, session exchange — none of which the
// already-implemented Service.Accept does on the caller's behalf, see
// AcceptInput's doc comment) and WorkspaceService to compute the post-accept
// workspace/session state the same way the ordinary sign-in path does.
type HandlersConfig struct {
	Service          *Service
	Adapter          *stytch.Adapter
	WorkspaceService *workspaces.WorkspaceService
}

// NewHTTPHandlers builds the workspaces.InvitationHandlers bundle that
// workspaces.RegisterRoutes wires onto the seven invitation routes. Handlers
// live in this package (not in workspaces) because Service depends on
// workspaces types; workspaces importing invitations back would cycle. See
// workspaces/invitation_handlers.go for the full explanation.
func NewHTTPHandlers(cfg HandlersConfig) workspaces.InvitationHandlers {
	return workspaces.InvitationHandlers{
		Resolve: resolveHandler(cfg),
		Decline: declineHandler(cfg),
		Accept:  acceptHandler(cfg),
		List:    listHandler(cfg),
		Create:  createHandler(cfg),
		Resend:  resendHandler(cfg),
		Revoke:  revokeHandler(cfg),
	}
}

// --- Public endpoints ---------------------------------------------------

// resolveHandler serves GET /invitations/:token (Req 11.1, 11.18).
func resolveHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		token := c.Param("token")
		ip := ratelimitSubjectIP(c)

		view, err := cfg.Service.Resolve(c.Request().Context(), token, ip)
		if err != nil {
			return respondInvitationError(c, err)
		}
		return responses.SendOK(c, view, "Invitation resolved")
	}
}

// declineHandler serves POST /invitations/:token/decline (Req 12.5, 12.6).
// The token is the sole authorization — no session is required.
func declineHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		token := c.Param("token")

		// Loaded before Decline() clears token_hash, purely to notify the
		// inviter (Req 12.5) — Decline() itself does not send that
		// notification; the doc comment on Decline says the caller is
		// responsible for it.
		inv, findErr := findInvitationByToken(ctx, token)

		if err := cfg.Service.Decline(ctx, token); err != nil {
			return respondInvitationError(c, err)
		}

		if findErr == nil {
			notifyInviter(ctx, inv.InvitedBy, inv.WorkspaceID, inv.Email, inv.Role, "declined")
		}

		return responses.SendOK(c, map[string]any{"declined": true}, "Invitation declined")
	}
}

// discoveryAuthRequest carries the Stytch discovery credential the invitee
// presents to prove they hold the invited mailbox. Field names mirror the
// existing /employer/signup/authenticate request shape in
// workspaces/routes.go for consistency.
type discoveryAuthRequest struct {
	DiscoveryToken string `json:"discovery_token"`
	Type           string `json:"type"`
}

// acceptHandler serves POST /invitations/:token/accept (Req 11.3-11.17).
//
// Design decision (see task notes / commit message for the full reasoning):
// Service.Accept takes an already-resolved UserID and an already-created
// StytchMemberID — it does not authenticate the invitee or create the
// Stytch member itself. That work has to happen here, in the HTTP layer,
// before Accept is called:
//
//  1. Authenticate the invitee via Stytch discovery (same primitive
//     onboarding.Service.Authenticate uses) to get a verified email.
//  2. Resolve an existing `users` document for that email, or create one
//     (mirrors onboarding.Service's own new-account path). Full account
//     linking (Req 11.7) is a multi-step challenge flow owned by
//     identity.LinkService / the onboarding group's routes — out of scope
//     for a single POST — so a match with no Account_Link asks the invitee
//     to sign in first instead of silently creating a duplicate account.
//  3. Create the Stytch Member in the invitation's target Organization
//     (Req 11.9); a failure here leaves the invitation pending and returns
//     503 without ever calling Accept (Req 11.10).
//  4. Call Service.Accept with the resolved UserID and new StytchMemberID.
//  5. Best-effort: exchange the discovery intermediate session for one
//     scoped to the target org (Req 11.16), mirroring the
//     StageComplete branch of the existing /employer/signup/authenticate
//     handler. A failure here does not undo the membership — the invitee
//     already joined; they just sign in normally afterwards.
func acceptHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		invitationToken := c.Param("token")

		var request discoveryAuthRequest
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest,
				"invalid_request", "invalid request body", "", "", nil)
		}
		discoveryToken := strings.TrimSpace(request.DiscoveryToken)
		if discoveryToken == "" {
			return responses.SendErrorWithCode(c, http.StatusBadRequest,
				"token_required", "discovery_token is required", "discovery_token", "", nil)
		}
		tokenType := strings.TrimSpace(request.Type)
		if tokenType == "" {
			tokenType = "magic_link"
		}

		// Load the raw invitation before spending a Stytch call: we need the
		// target Workspace's Stytch Organization for CreateMember, and this
		// also lets terminal statuses (revoked/expired/declined) short-circuit
		// without creating an orphaned Stytch member.
		inv, findErr := findInvitationByToken(ctx, invitationToken)
		if findErr != nil {
			if errors.Is(findErr, mongo.ErrNoDocuments) {
				return responses.SendErrorWithCode(c, http.StatusNotFound,
					"invitation_not_found", "no invitation matches the provided token", "", "", nil)
			}
			slog.ErrorContext(ctx, "invitations accept: find invitation", "error", findErr)
			return responses.SendErrorWithCode(c, http.StatusServiceUnavailable,
				"invitation_lookup_failed", "could not resolve the invitation", "", "try_again", nil)
		}

		switch EffectiveStatus(inv.Status, inv.ExpiresAt, cfg.Service.clock.Now()) {
		case StatusRevoked:
			return responses.SendErrorWithCode(c, http.StatusGone,
				"invitation_revoked", "this invitation has been revoked", "", "", nil)
		case StatusExpired:
			return responses.SendErrorWithCode(c, http.StatusGone,
				"invitation_expired", "this invitation has expired", "", "",
				map[string]any{"can_request_new": true})
		case StatusDeclined:
			return responses.SendErrorWithCode(c, http.StatusConflict,
				"invitation_declined", "this invitation has already been declined", "", "", nil)
		}

		var ws workspaces.Workspace
		if err := configs.MI.DB.Collection("workspaces").FindOne(ctx, bson.M{"_id": inv.WorkspaceID}).Decode(&ws); err != nil {
			slog.ErrorContext(ctx, "invitations accept: find workspace", "error", err)
			return responses.SendErrorWithCode(c, http.StatusServiceUnavailable,
				"invitation_lookup_failed", "could not resolve the invitation's workspace", "", "try_again", nil)
		}

		discoveryResult, authErr := authenticateDiscovery(ctx, cfg.Adapter, tokenType, discoveryToken)
		if authErr != nil {
			var badType *invalidTokenTypeError
			if errors.As(authErr, &badType) {
				return responses.SendErrorWithCode(c, http.StatusBadRequest,
					"invalid_token_type", badType.Error(), "type", "", nil)
			}
			slog.ErrorContext(ctx, "invitations accept: discovery authenticate failed", "error", authErr)
			return responses.SendErrorWithCode(c, http.StatusServiceUnavailable,
				"auth_provider_unavailable", "could not authenticate the invited person", "", "try_again", nil)
		}
		if !discoveryResult.EmailVerified {
			return responses.SendErrorWithCode(c, http.StatusForbidden,
				"email_verification_required", "email verification is required", "", "", nil)
		}

		authenticatedEmail := identity.NormalizeEmail(discoveryResult.Email)

		userID, newAccount, resolveErr := resolveOrCreateInviteeUser(ctx, authenticatedEmail)
		if resolveErr != nil {
			return respondInvitationError(c, resolveErr)
		}

		var member stytch.Member
		createErr := cfg.Adapter.Do(ctx, "CreateMember", true, func(attemptCtx context.Context) error {
			var err error
			member, err = cfg.Adapter.Client().CreateMember(attemptCtx, stytch.CreateMemberInput{
				OrganizationID: ws.StytchOrganizationID,
				Email:          authenticatedEmail,
				Roles:          []string{inv.Role},
			})
			return err
		})
		if createErr != nil {
			// Req 11.10: zero Memberships, invitation stays pending, 503.
			slog.ErrorContext(ctx, "invitations accept: create stytch member failed", "error", createErr)
			return responses.SendErrorWithCode(c, http.StatusServiceUnavailable,
				"auth_provider_unavailable", "could not create your account in this workspace", "", "try_again", nil)
		}

		result, acceptErr := cfg.Service.Accept(ctx, AcceptInput{
			Token:              invitationToken,
			AuthenticatedEmail: authenticatedEmail,
			UserID:             userID,
			StytchMemberID:     member.MemberID,
		})
		if acceptErr != nil {
			return respondInvitationError(c, acceptErr)
		}

		// A brand-new `users` document has no Account_Link yet — without one,
		// the ordinary session-resolution path (workspaces.Resolver.Resolve)
		// treats this person as unlinked forever (see resolver.go's
		// errUnlinkedMember check). Create it now, once, for exactly this
		// case; an existing user reusing an Account_Link from a prior
		// workspace keeps that original link (Req 5.8 — one link per user).
		if newAccount {
			if linkErr := createAccountLink(ctx, cfg.Service.clock, userID, member.MemberID, ws.StytchOrganizationID, authenticatedEmail); linkErr != nil {
				slog.ErrorContext(ctx, "invitations accept: create account link failed", "user_id", userID, "error", linkErr)
			}
		}

		if !result.AlreadyMember {
			notifyInviter(ctx, inv.InvitedBy, inv.WorkspaceID, authenticatedEmail, inv.Role, "accepted")
		}

		resp := acceptResponse{
			MembershipID:  result.MembershipID,
			WorkspaceID:   result.WorkspaceID,
			Role:          result.Role,
			AlreadyMember: result.AlreadyMember,
		}

		// Best-effort session exchange (Req 11.16) — see doc comment above.
		// A failure here does not roll back the membership just created.
		var sess stytch.Session
		exchangeErr := cfg.Adapter.Do(ctx, "ExchangeSession", true, func(attemptCtx context.Context) error {
			var err error
			sess, err = cfg.Adapter.Client().ExchangeIntermediateSession(attemptCtx, discoveryResult.IntermediateSessionToken, ws.StytchOrganizationID)
			return err
		})
		if exchangeErr != nil {
			slog.WarnContext(ctx, "invitations accept: session exchange failed; membership created without an immediate session", "error", exchangeErr)
			return responses.SendOK(c, resp, "Invitation accepted")
		}
		resp.SessionJWT = sess.SessionJWT

		postAuth, postAuthErr := cfg.WorkspaceService.ResolvePostAuth(ctx, userID, authenticatedEmail)
		if postAuthErr != nil {
			slog.WarnContext(ctx, "invitations accept: resolve post-auth failed", "error", postAuthErr)
			return responses.SendOK(c, resp, "Invitation accepted")
		}
		resp.PostAuth = &postAuth

		return responses.SendOK(c, resp, "Invitation accepted")
	}
}

// acceptResponse is the JSON body returned by POST /invitations/:token/accept.
type acceptResponse struct {
	MembershipID  string                     `json:"membership_id"`
	WorkspaceID   string                     `json:"workspace_id"`
	Role          string                     `json:"role"`
	AlreadyMember bool                       `json:"already_member,omitempty"`
	SessionJWT    string                     `json:"session_jwt,omitempty"`
	PostAuth      *workspaces.PostAuthResult `json:"post_auth,omitempty"`
}

// --- Workspace-scoped endpoints ------------------------------------------

// listHandler serves GET /workspaces/:workspaceID/invitations (Req 10.17).
func listHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := workspaces.FromEcho(c)
		if !ok {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		result, err := cfg.Service.List(c.Request().Context(), sc.WorkspaceID)
		if err != nil {
			return respondInvitationError(c, err)
		}
		return responses.SendOK(c, result, "Invitations listed")
	}
}

// createInvitationRequest is the request body for POST /workspaces/:workspaceID/invitations.
type createInvitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// createInvitationResponse extends the normal success envelope with an
// optional machine-readable `code`, used to surface
// invitation_created_delivery_pending (Req 10.16) without treating a
// successfully-created-but-undelivered invitation as an HTTP error.
type createInvitationResponse struct {
	responses.SuccessResponse
	Code string `json:"code,omitempty"`
}

// createHandler serves POST /workspaces/:workspaceID/invitations (Req 10.1-10.18).
func createHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		sc, ok := workspaces.FromEcho(c)
		if !ok {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}

		var request createInvitationRequest
		if err := c.Bind(&request); err != nil {
			return responses.SendErrorWithCode(c, http.StatusBadRequest,
				"invalid_request", "invalid request body", "", "", nil)
		}

		result, err := cfg.Service.Create(ctx, CreateInput{
			WorkspaceID: sc.WorkspaceID,
			Email:       request.Email,
			Role:        request.Role,
			InvitedBy:   sc.UserID,
		})
		if err != nil {
			return respondInvitationError(c, err)
		}

		status := http.StatusCreated
		if result.Updated {
			status = http.StatusOK
		}

		// Create() upserts and returns the plaintext Token for the email; it
		// does not send the email itself (see service.go's Create doc
		// comment, step 8-9 — the audit entry is written there, delivery is
		// not). Sending is this HTTP layer's job, mirroring how
		// onboarding.Service.InitiateMagicLink hands its own email off to the
		// Stytch adapter — the equivalent primitive here is
		// notifications.SendEmailViaSendGrid, the same function
		// notifications/email.go exposes for every other transactional email
		// in this codebase.
		code := ""
		if sendErr := sendInvitationEmail(ctx, sc.WorkspaceID, result.Invitation, result.Token); sendErr != nil {
			slog.WarnContext(ctx, "invitations create: send invitation email failed", "error", sendErr)
			status = http.StatusAccepted
			code = "invitation_created_delivery_pending"
		}

		return c.JSON(status, createInvitationResponse{
			SuccessResponse: responses.SuccessResponse{
				Success: true,
				Data:    result.Invitation,
				Message: "Invitation created",
			},
			Code: code,
		})
	}
}

// resendHandler serves POST /workspaces/:workspaceID/invitations/:id/resend (Req 12.3, 12.4, 12.9, 12.11).
func resendHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		sc, ok := workspaces.FromEcho(c)
		if !ok {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		invitationID := c.Param("id")

		token, err := cfg.Service.Resend(ctx, sc.WorkspaceID, invitationID, sc.UserID)
		if err != nil {
			return respondInvitationError(c, err)
		}

		// Resend() only mints the new token — reload the invitation to send
		// the refreshed expiry in the resend email. A send failure is
		// logged and swallowed rather than surfaced as an HTTP error: the
		// resend itself already succeeded and is independently repeatable
		// (Req 12.4 allows up to 3/hour), unlike Create's one-shot delivery,
		// so there is no equivalent "delivery_pending" code defined for this
		// endpoint.
		inv, findErr := findInvitationByID(ctx, sc.WorkspaceID, invitationID)
		if findErr == nil {
			if sendErr := sendInvitationEmail(ctx, sc.WorkspaceID, inv, token); sendErr != nil {
				slog.WarnContext(ctx, "invitations resend: send invitation email failed", "error", sendErr)
			}
		} else {
			slog.WarnContext(ctx, "invitations resend: reload invitation for email failed", "error", findErr)
		}

		return responses.SendOK(c, map[string]any{"resent": true}, "Invitation resent")
	}
}

// revokeHandler serves DELETE /workspaces/:workspaceID/invitations/:id (Req 12.2, 12.9, 12.12).
func revokeHandler(cfg HandlersConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		sc, ok := workspaces.FromEcho(c)
		if !ok {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		invitationID := c.Param("id")

		if err := cfg.Service.Revoke(c.Request().Context(), sc.WorkspaceID, invitationID, sc.UserID); err != nil {
			return respondInvitationError(c, err)
		}
		return responses.SendOK(c, map[string]any{"revoked": true}, "Invitation revoked")
	}
}

// --- Shared helpers -------------------------------------------------------

// respondInvitationError maps a Service error to the HTTP response. Every
// Service method in this package returns *InvitationError for expected
// failures; anything else is an unexpected internal error.
func respondInvitationError(c echo.Context, err error) error {
	var invErr *InvitationError
	if errors.As(err, &invErr) {
		return responses.SendErrorWithCode(c, invErr.Status, invErr.Code, invErr.Message, invErr.Field, "", invErr.Details)
	}
	slog.ErrorContext(c.Request().Context(), "invitations: unexpected error", "error", err)
	return responses.SendErrorWithCode(c, http.StatusInternalServerError,
		"internal_error", "an unexpected error occurred", "", "", nil)
}

// ratelimitSubjectIP mirrors ratelimit.SubjectIP without importing the
// ratelimit package here — Resolve's rate limiting is already applied by the
// echo.MiddlewareFunc registered alongside this handler in
// workspaces/routes.go; this is purely the subject Resolve records against.
func ratelimitSubjectIP(c echo.Context) string {
	return c.RealIP()
}

// findInvitationByToken looks up the raw Invitation document by plaintext
// token. Unlike Resolve/Decline/Accept's own internal lookups, this is used
// by the HTTP layer for data the public PublicView deliberately omits
// (WorkspaceID) or for notifications after the token has already been
// invalidated by a state transition.
func findInvitationByToken(ctx context.Context, token string) (Invitation, error) {
	var inv Invitation
	err := configs.MI.DB.Collection("invitations").FindOne(ctx, bson.M{"token_hash": hashToken(token)}).Decode(&inv)
	return inv, err
}

// findInvitationByID looks up the raw Invitation document by ID, scoped to a
// workspace — used after Resend/Revoke to reload fields those methods don't
// return directly.
func findInvitationByID(ctx context.Context, workspaceID, invitationID string) (Invitation, error) {
	var inv Invitation
	err := configs.MI.DB.Collection("invitations").FindOne(ctx, bson.M{
		"_id":          invitationID,
		"workspace_id": workspaceID,
	}).Decode(&inv)
	return inv, err
}

// sendInvitationEmail composes and sends the invitation email for a
// newly-created or resent invitation. workspaceID is used only to load the
// workspace's display name; inv already carries the invitee email, role,
// and (refreshed) expiry.
func sendInvitationEmail(ctx context.Context, workspaceID string, inv Invitation, plaintextToken string) error {
	var ws workspaces.Workspace
	if err := configs.MI.DB.Collection("workspaces").FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&ws); err != nil {
		return fmt.Errorf("send invitation email: load workspace: %w", err)
	}

	inviterName := "A team member"
	if inv.InvitedBy != "" {
		if inviter, err := users.GetUserByID(ctx, inv.InvitedBy); err == nil && inviter != nil {
			if name := buildDisplayName(inviter.FirstName, inviter.LastName); name != "" {
				inviterName = name
			}
		}
	}

	subject, body := BuildInvitationEmail(ws.Name, inviterName, inv.Role, inv.ExpiresAt)

	// BuildInvitationEmail (already implemented, already tested) composes
	// the plain-language summary but carries no link — the plaintext token
	// exists only in this email and the acceptance URL (Req 10.8), so the
	// link is appended here rather than inside that function.
	appURL := strings.TrimRight(configs.AppPublicURL(), "/")
	if appURL != "" {
		acceptURL := appURL + "/invitations/accept?token=" + url.QueryEscape(plaintextToken)
		body = fmt.Sprintf("%s\n\nAccept your invitation: %s", body, acceptURL)
	}

	return notifications.SendEmailViaSendGrid(ctx, inv.Email, subject, body)
}

// notifyInviter sends the inviter a best-effort email naming what happened
// to their invitation (Req 11.15, 12.5). Failures are logged, never
// propagated — the invitation state change itself already succeeded.
func notifyInviter(ctx context.Context, invitedByUserID, workspaceID, invitedEmail, role, event string) {
	if invitedByUserID == "" {
		return
	}
	inviter, err := users.GetUserByID(ctx, invitedByUserID)
	if err != nil || inviter == nil || inviter.Email == "" {
		if err != nil {
			slog.WarnContext(ctx, "invitations: notify inviter: load inviter failed", "error", err)
		}
		return
	}

	var ws workspaces.Workspace
	workspaceName := "your workspace"
	if err := configs.MI.DB.Collection("workspaces").FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&ws); err == nil {
		workspaceName = ws.Name
	}

	subject := fmt.Sprintf("Your invitation to %s was %s", workspaceName, event)
	body := fmt.Sprintf("%s %s the invitation to join %s as %s.", invitedEmail, event, workspaceName, role)

	if sendErr := notifications.SendEmailViaSendGrid(ctx, inviter.Email, subject, body); sendErr != nil {
		slog.WarnContext(ctx, "invitations: notify inviter: send email failed", "error", sendErr)
	}
}

// invalidTokenTypeError reports an unsupported discovery token `type`.
type invalidTokenTypeError struct {
	tokenType string
}

func (e *invalidTokenTypeError) Error() string {
	return fmt.Sprintf("unsupported token type: %q; supported: magic_link, oauth", e.tokenType)
}

// authenticateDiscovery redeems a Stytch discovery credential, mirroring the
// exact switch onboarding.Service.Authenticate uses for its own
// magic_link/oauth token types. It is reimplemented here (not delegated to
// onboarding.Service) because that service's Authenticate has onboarding-
// specific side effects — upserting onboarding_states, driving the
// brand-new-employer-signup state machine — that do not apply to someone
// simply accepting an invitation to an existing workspace.
func authenticateDiscovery(ctx context.Context, adapter *stytch.Adapter, tokenType, discoveryToken string) (stytch.DiscoveryResult, error) {
	var result stytch.DiscoveryResult
	var err error

	switch tokenType {
	case "magic_link":
		err = adapter.Do(ctx, "DiscoveryAuthenticate", false, func(attemptCtx context.Context) error {
			var innerErr error
			result, innerErr = adapter.Client().DiscoveryAuthenticate(attemptCtx, discoveryToken)
			return innerErr
		})
	case "oauth":
		err = adapter.Do(ctx, "OAuthDiscoveryAuthenticate", false, func(attemptCtx context.Context) error {
			var innerErr error
			result, innerErr = adapter.Client().OAuthDiscoveryAuthenticate(attemptCtx, discoveryToken)
			return innerErr
		})
	default:
		return stytch.DiscoveryResult{}, &invalidTokenTypeError{tokenType: tokenType}
	}

	if err != nil {
		return stytch.DiscoveryResult{}, fmt.Errorf("authenticate discovery: %w", err)
	}
	return result, nil
}

// resolveOrCreateInviteeUser resolves the `users` document backing the
// invitee's normalized email, applying Req 11.6 and 11.8. Req 11.7 (linking
// an existing, unlinked account) is a multi-step Link_Service challenge flow
// — out of scope for a single POST — so a match with no Account_Link returns
// a 409 asking the invitee to sign in normally first, rather than silently
// creating a second `users` document for the same mailbox.
func resolveOrCreateInviteeUser(ctx context.Context, normalizedEmail string) (userID string, newAccount bool, err error) {
	existing, err := users.GetUsersByNormalizedEmail(ctx, normalizedEmail)
	if err != nil {
		return "", false, fmt.Errorf("resolve invitee user: search existing users: %w", err)
	}

	accountLinksColl := configs.MI.DB.Collection("account_links")
	for _, u := range existing {
		if u.UserType == "artisan" || u.UserType == "assessor" || u.UserType == "admin" {
			continue
		}
		var link identity.AccountLink
		linkErr := accountLinksColl.FindOne(ctx, bson.M{"user_id": u.ID}).Decode(&link)
		if linkErr == nil {
			// Req 11.6: existing users document with an Account_Link — reuse it.
			return u.ID, false, nil
		}
		if !errors.Is(linkErr, mongo.ErrNoDocuments) {
			return "", false, fmt.Errorf("resolve invitee user: check account link: %w", linkErr)
		}
	}

	if len(existing) > 0 {
		return "", false, &InvitationError{
			Status:  http.StatusConflict,
			Code:    "account_link_required",
			Message: "an account with this email already exists; please sign in before accepting this invitation",
		}
	}

	// Req 11.8: zero matches — create one users document, same shape as
	// onboarding.Service.Authenticate's own new-account path.
	newUser := &users.User{
		Email:    normalizedEmail,
		UserType: "customer",
		Status:   users.StatusActive,
		IsActive: true,
		Password: []byte{},
	}
	if createErr := users.CreateNewUser(newUser); createErr != nil {
		if mongo.IsDuplicateKeyError(createErr) {
			return "", false, &InvitationError{
				Status:  http.StatusConflict,
				Code:    "email_already_registered",
				Message: "this email is already registered under a different account type",
				Field:   "email",
			}
		}
		return "", false, fmt.Errorf("resolve invitee user: create user: %w", createErr)
	}
	return newUser.ID, true, nil
}

// createAccountLink records the primary association between a brand-new
// `users` document and the Stytch Member just created for it (Req 5.8).
// Without this, workspaces.Resolver.Resolve treats the new member as
// unlinked on every future request (see resolver.go's errUnlinkedMember
// check), which would make the just-accepted membership permanently
// unusable through the ordinary session-resolution path.
func createAccountLink(ctx context.Context, clock stytch.Clock, userID, stytchMemberID, stytchOrgID, linkedEmail string) error {
	now := clock.Now()
	link := identity.AccountLink{
		ID:                   uuid.New().String(),
		UserID:               userID,
		StytchMemberID:       stytchMemberID,
		StytchOrganizationID: stytchOrgID,
		LinkedEmail:          linkedEmail,
		LinkMethod:           "invitation_accept",
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	_, err := configs.MI.DB.Collection("account_links").InsertOne(ctx, link)
	return err
}
