package workspaces

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"backend/configs"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	migrationReportDefaultPageSize = 100
	migrationReportMaxPageSize     = 500

	// migrationUserPageSize bounds how many customer users are fetched from
	// Mongo per page while resolving migration candidates, per the batch
	// processing convention — the customer user set could grow beyond memory.
	migrationUserPageSize = 500
	migrationUserMaxPages = 200
)

// MigrationHandler serves the admin migration endpoints.
type MigrationHandler struct {
	job *MigrationJob
}

// NewMigrationHandler creates a MigrationHandler.
func NewMigrationHandler(job *MigrationJob) *MigrationHandler {
	return &MigrationHandler{job: job}
}

// HandleStartMigration is the handler for POST /admin/employer-migration.
// Behind middleware.IsAdmin. Triggers a migration run with the provided options.
//
// Request body:
//
//	{ "dry_run": true, "batch_size": 50, "limit": 0 }
//
// Requirements: 21.8, 21.16
func (h *MigrationHandler) HandleStartMigration(c echo.Context) error {
	var input struct {
		DryRun    bool `json:"dry_run"`
		BatchSize int  `json:"batch_size"`
		Limit     int  `json:"limit"`
	}
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"success": false,
			"message": "invalid request body",
		})
	}

	// Candidate resolution happens inline, so it is bound to the request: if
	// the admin walks away the scan must stop rather than keep chewing through
	// the customer user set.
	ctx := c.Request().Context()
	result, err := resolveMigrationCandidates(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "migration: candidate resolution failed", "error", err.Error())
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "failed to resolve migration candidates",
		})
	}

	opt := MigrationOptions{
		DryRun:    input.DryRun,
		BatchSize: input.BatchSize,
		Limit:     input.Limit,
	}

	// A migration run over many employers can take a while (throttled to
	// MaxRPS Stytch calls per second), so it runs detached from the request
	// context and the caller polls GET /admin/employer-migration/:runID for
	// progress instead of holding the connection open.
	runIDCh := make(chan string, 1)
	go func() {
		runID, runErr := h.job.Run(context.Background(), result.Eligible, opt)
		if runErr != nil {
			slog.ErrorContext(context.Background(), "migration: run failed to start", "error", runErr.Error())
			runIDCh <- ""
			return
		}
		runIDCh <- runID
	}()

	// Run() inserts the employer_migration_runs record synchronously before
	// starting the batch loop, so the run ID is available almost immediately;
	// wait briefly for it rather than guessing an ID here.
	var runID string
	select {
	case runID = <-runIDCh:
	case <-c.Request().Context().Done():
		return c.JSON(http.StatusRequestTimeout, map[string]any{
			"success": false,
			"message": "migration start timed out",
		})
	}
	// Run() signals an empty ID when the run record insert failed; returning
	// 202 with an empty run_id would send the admin polling an ID that will
	// never exist.
	if runID == "" {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "migration failed to start",
		})
	}

	collisionSummary := make([]map[string]any, 0, len(result.Collisions))
	for _, col := range result.Collisions {
		collisionSummary = append(collisionSummary, map[string]any{
			"normalized_email": col.NormalizedEmail,
			"user_ids":         col.UserIDs,
		})
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"success": true,
		"message": "migration started",
		"data": map[string]any{
			"run_id":            runID,
			"eligible_count":    len(result.Eligible),
			"collisions":        collisionSummary,
			"no_email_user_ids": result.NoEmail,
			// True means the candidate scan hit its page cap and this run does
			// not cover every customer user.
			"candidates_truncated": result.Truncated,
		},
	})
}

// resolveMigrationCandidates fetches every customer user in chunks and
// applies the Candidates eligibility rule (Req 21.2, 21.5, 21.6, 21.14,
// 21.15). Ownership and Account_Link lookups are batched via aggregation
// rather than issued once per user, since the customer user set can be large.
func resolveMigrationCandidates(ctx context.Context) (CandidatesResult, error) {
	var allUsers []users.User
	var page int64 = 1
	exhausted := false
	pages := 0
	for ; pages < migrationUserMaxPages; pages++ {
		batch, _, err := users.ListAllUsers(bson.M{"user_type": "customer"}, page, migrationUserPageSize)
		if err != nil {
			return CandidatesResult{}, err
		}
		if len(batch) == 0 {
			exhausted = true
			break
		}
		allUsers = append(allUsers, batch...)
		if int64(len(batch)) < migrationUserPageSize {
			exhausted = true
			break
		}
		page++
	}
	// Hitting the page cap means users beyond it were never considered for
	// migration. Silent truncation would read as "everybody was evaluated", so
	// this is both logged and reported back to the caller.
	truncated := !exhausted && pages == migrationUserMaxPages
	if truncated {
		slog.WarnContext(ctx, "migration: hit max page limit, some customer users were not evaluated",
			"max_pages", migrationUserMaxPages,
			"page_size", migrationUserPageSize,
			"users_scanned", len(allUsers),
		)
	}

	userIDs := make([]string, 0, len(allUsers))
	for _, u := range allUsers {
		userIDs = append(userIDs, u.ID)
	}

	linked, err := linkedUserIDSet(ctx, userIDs)
	if err != nil {
		return CandidatesResult{}, err
	}
	ownership, err := ownershipByUserID(ctx, userIDs)
	if err != nil {
		return CandidatesResult{}, err
	}

	candidates := Candidates(
		allUsers,
		func(userID string) OwnershipInfo { return ownership[userID] },
		func(userID string) bool { _, ok := linked[userID]; return ok },
	)
	candidates.Truncated = truncated
	return candidates, nil
}

// linkedUserIDSet returns the subset of userIDs that already hold an
// Account_Link (Req 21.5 excludes them from migration).
func linkedUserIDSet(ctx context.Context, userIDs []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(userIDs))
	if len(userIDs) == 0 {
		return set, nil
	}
	cursor, err := configs.MI.DB.Collection("account_links").Find(ctx, bson.M{"user_id": bson.M{"$in": userIDs}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	for cursor.Next(ctx) {
		var link struct {
			UserID string `bson:"user_id"`
		}
		if err := cursor.Decode(&link); err != nil {
			return nil, err
		}
		set[link.UserID] = struct{}{}
	}
	return set, cursor.Err()
}

// ownershipByUserID aggregates company and vacancy counts, plus the newest
// company name, per owner_id (Req 21.3, 21.15).
func ownershipByUserID(ctx context.Context, userIDs []string) (map[string]OwnershipInfo, error) {
	result := make(map[string]OwnershipInfo, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}

	companiesColl := configs.MI.DB.Collection("companies")
	cursor, err := companiesColl.Find(ctx,
		bson.M{"owner_id": bson.M{"$in": userIDs}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	seenNewest := make(map[string]bool, len(userIDs))
	for cursor.Next(ctx) {
		var co struct {
			OwnerID string `bson:"owner_id"`
			Name    string `bson:"name"`
		}
		if err := cursor.Decode(&co); err != nil {
			return nil, err
		}
		info := result[co.OwnerID]
		info.Companies++
		if !seenNewest[co.OwnerID] {
			info.NewestCompanyName = co.Name
			seenNewest[co.OwnerID] = true
		}
		result[co.OwnerID] = info
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}

	vacanciesColl := configs.MI.DB.Collection("job_vacancies")
	vacCursor, err := vacanciesColl.Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"owner_id": bson.M{"$in": userIDs}}},
		bson.M{"$group": bson.M{"_id": "$owner_id", "count": bson.M{"$sum": 1}}},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = vacCursor.Close(ctx) }()

	for vacCursor.Next(ctx) {
		var v struct {
			OwnerID string `bson:"_id"`
			Count   int    `bson:"count"`
		}
		if err := vacCursor.Decode(&v); err != nil {
			return nil, err
		}
		info := result[v.OwnerID]
		info.Vacancies = v.Count
		result[v.OwnerID] = info
	}
	return result, vacCursor.Err()
}

// HandleGetMigrationReport is the handler for GET /admin/employer-migration/:runID.
// Behind middleware.IsAdmin. Returns the run summary and findings.
//
// Requirements: 21.8, 21.16
func (h *MigrationHandler) HandleGetMigrationReport(c echo.Context) error {
	runID := c.Param("runID")
	if runID == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"success": false,
			"message": "runID is required",
		})
	}

	// Fetch the run record.
	runsColl := configs.MI.DB.Collection("employer_migration_runs")
	var run MigrationRun
	err := runsColl.FindOne(c.Request().Context(), bson.M{"_id": runID}).Decode(&run)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return c.JSON(http.StatusNotFound, map[string]any{
				"success": false,
				"message": "migration run not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "failed to fetch migration run",
		})
	}

	// Fetch findings for this run, paginated — a run can have thousands of
	// candidates and returning them all in one response is a memory spike and
	// a latency bomb for the admin UI.
	page := 1
	if p := c.QueryParam("page"); p != "" {
		if v, parseErr := strconv.Atoi(p); parseErr == nil && v > 0 {
			page = v
		}
	}
	pageSize := migrationReportDefaultPageSize
	if ps := c.QueryParam("page_size"); ps != "" {
		if v, parseErr := strconv.Atoi(ps); parseErr == nil && v > 0 && v <= migrationReportMaxPageSize {
			pageSize = v
		}
	}

	findingsColl := configs.MI.DB.Collection("employer_migration_findings")
	total, err := findingsColl.CountDocuments(c.Request().Context(), bson.M{"run_id": runID})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "failed to count migration findings",
		})
	}

	cursor, err := findingsColl.Find(c.Request().Context(), bson.M{"run_id": runID},
		options.Find().
			SetSkip(int64((page-1)*pageSize)).
			SetLimit(int64(pageSize)),
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "failed to fetch migration findings",
		})
	}
	defer func() { _ = cursor.Close(c.Request().Context()) }()

	var findings []MigrationFinding
	if err := cursor.All(c.Request().Context(), &findings); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "failed to decode migration findings",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"run":       run,
			"findings":  findings,
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}
