package workspaces

import (
	"context"
	"errors"
	"fmt"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
)

// RescopeReport records the outcome of a re-scoping operation.
type RescopeReport struct {
	Companies     int64 `json:"companies"`
	Vacancies     int64 `json:"vacancies"`
	Subscriptions int64 `json:"subscriptions"`
}

// RescopeUserData moves a user's unscoped companies, job_vacancies, and
// user_subscriptions into the specified workspace. This is idempotent —
// applying it twice leaves documents unchanged (Req 6.5).
//
// Implementation:
//   - Idempotent updateMany per collection with a filter excluding already-scoped rows
//   - Write companies, job_vacancies, and user_subscriptions DIRECTLY via configs.MI.DB
//     so no workspaces -> jobrequests or workspaces -> subscriptions import is created
//   - Retain every owner_id (Req 6.4)
//   - On partial failure, record it, leave workspace usable, set rescope_pending (Req 6.6)
//   - Apply zero deletions to quotations, reviews, transactions, or notifications (Req 6.10)
//
// Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 6.10
//
// Each collection's update is attempted regardless of whether an earlier one
// failed: returning immediately on the first error (as an earlier version of
// this function did) means a failure on companies leaves vacancies and
// subscriptions untouched even though nothing prevents attempting them. On
// any failure, rescope_pending is set on the workspace so the scheduler (or
// an operator) can retry — the update is idempotent per collection because
// the filter excludes documents that already carry workspace_id.
func (w *WorkspaceService) RescopeUserData(ctx context.Context, userID, workspaceID string) (RescopeReport, error) {
	var report RescopeReport
	var errs []error

	// Re-scope companies: set workspace_id where owner_id matches and workspace_id is absent.
	companiesResult, err := configs.MI.DB.Collection("companies").UpdateMany(ctx,
		bson.M{
			"owner_id":     userID,
			"workspace_id": bson.M{"$exists": false},
		},
		bson.M{"$set": bson.M{"workspace_id": workspaceID}},
	)
	if err != nil {
		errs = append(errs, fmt.Errorf("rescope companies: %w", err))
	} else {
		report.Companies = companiesResult.ModifiedCount
	}

	// Re-scope job_vacancies: same pattern.
	vacanciesResult, err := configs.MI.DB.Collection("job_vacancies").UpdateMany(ctx,
		bson.M{
			"owner_id":     userID,
			"workspace_id": bson.M{"$exists": false},
		},
		bson.M{"$set": bson.M{"workspace_id": workspaceID}},
	)
	if err != nil {
		errs = append(errs, fmt.Errorf("rescope vacancies: %w", err))
	} else {
		report.Vacancies = vacanciesResult.ModifiedCount
	}

	// Re-scope user_subscriptions: filter by user_id and absent workspace_id.
	subsResult, err := configs.MI.DB.Collection("user_subscriptions").UpdateMany(ctx,
		bson.M{
			"user_id":      userID,
			"workspace_id": bson.M{"$exists": false},
			"audience":     "employer", // only employer subscriptions (Req 6.3)
		},
		bson.M{"$set": bson.M{"workspace_id": workspaceID}},
	)
	if err != nil {
		errs = append(errs, fmt.Errorf("rescope subscriptions: %w", err))
	} else {
		report.Subscriptions = subsResult.ModifiedCount
	}

	if len(errs) > 0 {
		if _, flagErr := configs.MI.DB.Collection("workspaces").UpdateOne(ctx,
			bson.M{"_id": workspaceID},
			bson.M{"$set": bson.M{"rescope_pending": true, "updated_at": w.clock.Now()}},
		); flagErr != nil {
			errs = append(errs, fmt.Errorf("rescope: failed to set rescope_pending: %w", flagErr))
		}
		return report, errors.Join(errs...)
	}

	return report, nil
}
