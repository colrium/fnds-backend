package workspaces

import (
	"context"
	"fmt"
	"strings"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/ratelimit"
	"backend/stytch"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// WorkspaceService manages workspace lifecycle.
type WorkspaceService struct {
	adapter *stytch.Adapter
	audit   *audit.Logger
	clock   stytch.Clock
	limiter ratelimit.RateLimiter
}

// maxSlugRetryAttempts bounds the slug-suffix retry loop in
// insertWorkspaceWithSlugRetry.
const maxSlugRetryAttempts = 100

// NewWorkspaceService constructs a WorkspaceService.
func NewWorkspaceService(adapter *stytch.Adapter, auditLogger *audit.Logger, clock stytch.Clock, limiter ratelimit.RateLimiter) *WorkspaceService {
	return &WorkspaceService{
		adapter: adapter,
		audit:   auditLogger,
		clock:   clock,
		limiter: limiter,
	}
}

// insertWorkspaceWithSlugRetry inserts ws, retrying the slug with -2, -3, ...
// up to maxSlugRetryAttempts on mongo.IsDuplicateKeyError. Shared by
// CreateForOnboarding and CreateAdditional so the retry policy has one
// definition instead of two copies that can drift apart. Mutates ws.Slug to
// the slug that was actually inserted and returns it.
func (w *WorkspaceService) insertWorkspaceWithSlugRetry(ctx context.Context, ws *Workspace, baseSlug string) (string, error) {
	workspacesColl := configs.MI.DB.Collection("workspaces")

	var insertedSlug string
	var slugErr error

	for attempt := 1; attempt <= maxSlugRetryAttempts; attempt++ {
		candidate := baseSlug
		if attempt > 1 {
			candidate = fmt.Sprintf("%s-%d", baseSlug, attempt)
		}

		ws.Slug = candidate
		_, slugErr = workspacesColl.InsertOne(ctx, ws)
		if slugErr == nil {
			insertedSlug = candidate
			break
		}
		if !mongo.IsDuplicateKeyError(slugErr) {
			return "", fmt.Errorf("insert workspace: %w", slugErr)
		}
		// Duplicate key on slug — try the next suffix.
	}

	if insertedSlug == "" {
		return "", fmt.Errorf("could not reserve unique slug after %d attempts", maxSlugRetryAttempts)
	}

	ws.Slug = insertedSlug
	return insertedSlug, nil
}

// CreateForOnboardingInput carries the parameters for CreateForOnboarding.
type CreateForOnboardingInput struct {
	OnboardingStateID   string
	StytchOrgID         string // reused if non-empty (Req 3.3)
	IntermediateSession string
	UserID              string
	Email               string
	StytchMemberID      string
	RawName             string
}

// CreateForOnboardingResult is returned on successful workspace creation.
type CreateForOnboardingResult struct {
	Workspace  Workspace
	Membership Membership
}

// CreateForOnboarding provisions a workspace during employer onboarding.
//
// Implementation steps:
//  1. Validate and normalize the workspace name
//  2. Derive the slug
//  3. If StytchOrgID is already set, reuse it (Req 3.3) — make zero Stytch creates
//  4. Otherwise, create a Stytch Organization via DiscoveryOrganizationCreate
//     with JIT=RESTRICTED, invites=ALL_ALLOWED, idempotency_key="onboarding:"+stateID
//  5. Insert the workspace document, retrying slug with -2, -3, ... up to 100 attempts
//     on mongo.IsDuplicateKeyError (same pattern as users.PublishWithUniqueSlug)
//  6. Insert the owner membership with authority_version=1
//  7. Return the created workspace and membership
//
// Requirements: 2.1, 2.8, 2.10, 2.11, 2.12, 2.13, 2.14, 2.17, 3.2, 3.3, 3.4, 3.5, 3.10, 3.12
func (w *WorkspaceService) CreateForOnboarding(ctx context.Context, in CreateForOnboardingInput) (CreateForOnboardingResult, error) {
	// Step 1: Validate the workspace name (Req 2.2–2.5).
	if err := ValidateName(in.RawName); err != nil {
		return CreateForOnboardingResult{}, fmt.Errorf("validation: %w", err)
	}

	// Normalize the name (Req 2.6).
	normalized := NormalizeName(in.RawName)

	// Step 2: Derive the base slug (Req 2.7, 2.9).
	baseSlug := DeriveSlug(normalized)

	// Step 3/4: Resolve the Stytch Organization ID.
	stytchOrgID := in.StytchOrgID
	stytchMemberID := in.StytchMemberID

	if stytchOrgID == "" {
		// No existing org — create one via the adapter (Req 2.13, 3.12).
		var createResult stytch.CreateOrgResult
		err := w.adapter.Do(ctx, "CreateOrganizationFromDiscovery", false, func(attemptCtx context.Context) error {
			var callErr error
			createResult, callErr = w.adapter.Client().CreateOrganizationFromDiscovery(attemptCtx, stytch.CreateOrgInput{
				IntermediateSessionToken: in.IntermediateSession,
				Name:                     normalized,
				Slug:                     baseSlug,
				EmailJITProvisioning:     "RESTRICTED",
				EmailInvites:             "ALL_ALLOWED",
				IdempotencyKey:           "onboarding:" + in.OnboardingStateID,
			})
			return callErr
		})
		if err != nil {
			return CreateForOnboardingResult{}, fmt.Errorf("stytch create organization: %w", err)
		}
		stytchOrgID = createResult.OrganizationID
		// Use the member ID returned by Stytch if we didn't already have one.
		if stytchMemberID == "" {
			stytchMemberID = createResult.MemberID
		}
	}

	// Step 5: Insert the workspace document with slug retry (Req 2.8, 2.10).
	now := w.clock.Now().UTC()
	workspaceID := uuid.New().String()

	ws := Workspace{
		ID:                   workspaceID,
		Name:                 normalized,
		NameNormalized:       strings.ToLower(normalized),
		Slug:                 baseSlug, // will be updated per attempt
		StytchOrganizationID: stytchOrgID,
		CreatedBy:            in.UserID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	insertedSlug, err := w.insertWorkspaceWithSlugRetry(ctx, &ws, baseSlug)
	if err != nil {
		return CreateForOnboardingResult{}, err
	}

	// Step 6: Insert the owner membership with authority_version=1 (Req 2.14, 3.10).
	membershipID := uuid.New().String()
	membership := Membership{
		ID:               membershipID,
		WorkspaceID:      workspaceID,
		UserID:           in.UserID,
		Role:             RoleOwner,
		Status:           MembershipActive,
		AuthorityVersion: 1,
		StytchMemberID:   stytchMemberID,
		JoinedAt:         now,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	membershipsColl := configs.MI.DB.Collection("memberships")
	if _, err := membershipsColl.InsertOne(ctx, membership); err != nil {
		return CreateForOnboardingResult{}, fmt.Errorf("insert membership: %w", err)
	}

	// Step 7: Write one audit entry for workspace creation (Req 23.1).
	w.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: in.UserID,
		ActorKind:   "member",
		Action:      audit.ActionWorkspaceCreated,
		TargetKind:  "workspace",
		TargetID:    workspaceID,
		IP:          "", // IP is set by the caller (route handler) if available
		New: map[string]any{
			"name": normalized,
			"slug": insertedSlug,
		},
	})

	return CreateForOnboardingResult{
		Workspace:  ws,
		Membership: membership,
	}, nil
}

// WorkspaceSummary is the public representation for ListForUser.
type WorkspaceSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Role        Role      `json:"role"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// ByID returns the workspace with the given ID, or a *WorkspaceError with
// Status 404 when it does not exist.
func (w *WorkspaceService) ByID(ctx context.Context, workspaceID string) (Workspace, error) {
	var ws Workspace
	err := configs.MI.DB.Collection("workspaces").FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&ws)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return Workspace{}, &WorkspaceError{Status: 404, Code: "workspace_not_found", Message: "workspace not found"}
		}
		return Workspace{}, fmt.Errorf("get workspace: %w", err)
	}
	return ws, nil
}

// Rename changes the workspace name. Retains the existing slug, propagates
// the new name to the Stytch organization. (Req 2.15, 2.16)
func (w *WorkspaceService) Rename(ctx context.Context, workspaceID, rawName, actorUserID string) error {
	// Step 1: Validate the new name.
	if err := ValidateName(rawName); err != nil {
		return &WorkspaceError{Status: 400, Code: "invalid_workspace_name", Message: err.Error()}
	}

	// Step 2: Normalize the name.
	normalized := NormalizeName(rawName)

	// Step 3: Update workspace in DB: name, name_normalized, updated_at.
	now := w.clock.Now().UTC()
	workspacesColl := configs.MI.DB.Collection("workspaces")

	filter := bson.M{"_id": workspaceID}
	update := bson.M{
		"$set": bson.M{
			"name":            normalized,
			"name_normalized": strings.ToLower(normalized),
			"updated_at":      now,
		},
	}

	var previous Workspace
	err := workspacesColl.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&previous)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &WorkspaceError{Status: 404, Code: "workspace_not_found", Message: "workspace not found"}
		}
		return fmt.Errorf("update workspace name: %w", err)
	}

	// Step 4: Propagate to Stytch: UpdateOrganization(orgID, {Name: normalized}).
	if previous.StytchOrganizationID != "" {
		stytchCtx := stytch.ContextWithWorkspaceID(ctx, workspaceID)
		err = w.adapter.Do(stytchCtx, "UpdateOrganization", false, func(attemptCtx context.Context) error {
			return w.adapter.Client().UpdateOrganization(attemptCtx, previous.StytchOrganizationID, stytch.UpdateOrgInput{
				Name: normalized,
			})
		})
		if err != nil {
			// Log and continue — Stytch mirroring failure is a warning, not a rollback.
			// The Fundis DB is the source of truth for workspace names.
			_ = err
		}
	}

	// Step 5: Write audit entry for rename.
	w.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: actorUserID,
		ActorKind:   "member",
		Action:      audit.ActionWorkspaceRenamed,
		TargetKind:  "workspace",
		TargetID:    workspaceID,
		Previous: map[string]any{
			"name": previous.Name,
		},
		New: map[string]any{
			"name": normalized,
		},
	})

	return nil
}

// ListForUser returns all workspaces where the user holds an active membership.
// (Req 15.10)
func (w *WorkspaceService) ListForUser(ctx context.Context, userID string) ([]WorkspaceSummary, error) {
	membershipsColl := configs.MI.DB.Collection("memberships")
	workspacesColl := configs.MI.DB.Collection("workspaces")

	// Step 1: Query memberships where user_id=userID and status=active.
	filter := bson.M{"user_id": userID, "status": string(MembershipActive)}
	cursor, err := membershipsColl.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var memberships []Membership
	if err := cursor.All(ctx, &memberships); err != nil {
		return nil, fmt.Errorf("decode memberships: %w", err)
	}

	if len(memberships) == 0 {
		return []WorkspaceSummary{}, nil
	}

	// Collect workspace IDs and build role lookup.
	workspaceIDs := make([]string, 0, len(memberships))
	roleByWorkspace := make(map[string]Role, len(memberships))
	for _, m := range memberships {
		workspaceIDs = append(workspaceIDs, m.WorkspaceID)
		roleByWorkspace[m.WorkspaceID] = m.Role
	}

	// Step 2: Fetch workspace details.
	wsCursor, err := workspacesColl.Find(ctx, bson.M{"_id": bson.M{"$in": workspaceIDs}})
	if err != nil {
		return nil, fmt.Errorf("find workspaces: %w", err)
	}
	defer func() { _ = wsCursor.Close(ctx) }()

	var workspaces []Workspace
	if err := wsCursor.All(ctx, &workspaces); err != nil {
		return nil, fmt.Errorf("decode workspaces: %w", err)
	}

	wsMap := make(map[string]Workspace, len(workspaces))
	for _, ws := range workspaces {
		wsMap[ws.ID] = ws
	}

	// Step 3: Count active members per workspace.
	memberCountPipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"workspace_id": bson.M{"$in": workspaceIDs},
			"status":       string(MembershipActive),
		}}},
		{{Key: "$group", Value: bson.M{
			"_id":   "$workspace_id",
			"count": bson.M{"$sum": 1},
		}}},
	}

	countCursor, err := membershipsColl.Aggregate(ctx, memberCountPipeline)
	if err != nil {
		return nil, fmt.Errorf("count members: %w", err)
	}
	defer func() { _ = countCursor.Close(ctx) }()

	type countResult struct {
		ID    string `bson:"_id"`
		Count int    `bson:"count"`
	}
	var counts []countResult
	if err := countCursor.All(ctx, &counts); err != nil {
		return nil, fmt.Errorf("decode member counts: %w", err)
	}

	countByWorkspace := make(map[string]int, len(counts))
	for _, c := range counts {
		countByWorkspace[c.ID] = c.Count
	}

	// Step 4: Build and return WorkspaceSummary list.
	summaries := make([]WorkspaceSummary, 0, len(memberships))
	for _, m := range memberships {
		ws, ok := wsMap[m.WorkspaceID]
		if !ok {
			// Workspace document missing — skip this membership.
			continue
		}
		summaries = append(summaries, WorkspaceSummary{
			ID:          ws.ID,
			Name:        ws.Name,
			Slug:        ws.Slug,
			Role:        roleByWorkspace[m.WorkspaceID],
			MemberCount: countByWorkspace[m.WorkspaceID],
			CreatedAt:   ws.CreatedAt,
		})
	}

	return summaries, nil
}

// CreateAdditional creates an additional workspace for a user who already holds
// at least one active membership. Applies the same rules as CreateForOnboarding
// but without the Stytch discovery flow (uses the existing session instead).
// Caps at 5 workspaces per 24 hours per user. (Req 15.12, 15.13)
func (w *WorkspaceService) CreateAdditional(ctx context.Context, userID, rawName, stytchOrgID, stytchMemberID string) (CreateForOnboardingResult, error) {
	// Step 1: Rate limit — 5 per 24h per user (Req 15.13).
	if w.limiter != nil {
		decision := w.limiter.Allow(ctx, ratelimit.LimitWorkspaceCreate, userID)
		if !decision.Allowed {
			return CreateForOnboardingResult{}, &WorkspaceError{
				Status:     429,
				Code:       "workspace_create_rate_limited",
				Message:    "too many workspaces created recently, try again later",
				RetryAfter: decision.RetryAfter,
			}
		}
	}

	// Step 2: Validate and normalize the workspace name (same as CreateForOnboarding).
	if err := ValidateName(rawName); err != nil {
		return CreateForOnboardingResult{}, fmt.Errorf("validation: %w", err)
	}
	normalized := NormalizeName(rawName)
	baseSlug := DeriveSlug(normalized)

	// Step 3: Insert the workspace document with slug retry (reuses the slug-retry logic).
	now := w.clock.Now().UTC()
	workspaceID := uuid.New().String()

	ws := Workspace{
		ID:                   workspaceID,
		Name:                 normalized,
		NameNormalized:       strings.ToLower(normalized),
		Slug:                 baseSlug,
		StytchOrganizationID: stytchOrgID,
		CreatedBy:            userID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	insertedSlug, err := w.insertWorkspaceWithSlugRetry(ctx, &ws, baseSlug)
	if err != nil {
		return CreateForOnboardingResult{}, err
	}

	// Step 4: Insert the owner membership with authority_version=1.
	membershipID := uuid.New().String()
	membership := Membership{
		ID:               membershipID,
		WorkspaceID:      workspaceID,
		UserID:           userID,
		Role:             RoleOwner,
		Status:           MembershipActive,
		AuthorityVersion: 1,
		StytchMemberID:   stytchMemberID,
		JoinedAt:         now,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	membershipsColl := configs.MI.DB.Collection("memberships")
	if _, err := membershipsColl.InsertOne(ctx, membership); err != nil {
		return CreateForOnboardingResult{}, fmt.Errorf("insert membership: %w", err)
	}

	// Step 5: Write audit entry for workspace creation.
	w.audit.Write(ctx, audit.WriteInput{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorKind:   "member",
		Action:      audit.ActionWorkspaceCreated,
		TargetKind:  "workspace",
		TargetID:    workspaceID,
		New: map[string]any{
			"name": normalized,
			"slug": insertedSlug,
		},
	})

	return CreateForOnboardingResult{
		Workspace:  ws,
		Membership: membership,
	}, nil
}

// WorkspaceError is a structured error returned by WorkspaceService operations
// when a precondition or rate-limit check fails.
type WorkspaceError struct {
	Status     int           `json:"-"`
	Code       string        `json:"code"`
	Message    string        `json:"message"`
	RetryAfter time.Duration `json:"-"`
}

func (e *WorkspaceError) Error() string { return e.Message }
