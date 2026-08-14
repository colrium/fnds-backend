package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"backend/contextutil"
	"backend/notifications"
	"backend/profileapproval"
	"backend/profiles"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

var validProfileReviewStatuses = map[string]bool{
	users.ProfileStatusNotSubmitted:  true,
	users.ProfileStatusPendingReview: true,
	users.ProfileStatusApproved:      true,
	users.ProfileStatusRejected:      true,
}

// buildProfileReviewFilter builds the Mongo filter for the admin
// profile-review list. not_submitted must match both the literal empty
// string (users created before profile_status existed or the backfill ran)
// and the literal "not_submitted" value; every other status is an exact
// match since a user is never accidentally in one of those states.
func buildProfileReviewFilter(status string) bson.M {
	if status == users.ProfileStatusNotSubmitted {
		return bson.M{"profile_status": bson.M{"$in": bson.A{"", users.ProfileStatusNotSubmitted}}}
	}
	return bson.M{"profile_status": status}
}

func HandleListProfileReviews(c echo.Context) error {
	page, limit := parsePageLimit(c)
	status := c.QueryParam("status")
	if status == "" {
		status = users.ProfileStatusPendingReview
	} else if !validProfileReviewStatuses[status] {
		return responses.SendError(c, http.StatusBadRequest, "invalid status")
	}

	list, total, err := users.ListAllUsers(buildProfileReviewFilter(status), page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list profile reviews")
	}
	if list == nil {
		list = []users.User{}
	}
	return responses.SendOK(c, Page[users.User]{
		Data:  list,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Profile reviews retrieved")
}

func HandleGetProfileReview(c echo.Context) error {
	userID := c.Param("user_id")
	profile, err := profiles.GetFullProfile(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load profile")
	}
	return responses.SendOK(c, profile, "Profile retrieved")
}

// notifyProfileReviewDecision fires the artisan-facing notification for an
// approve/reject decision. notifications.NewDefaultService panics if
// ConfigureDefaultStore was never called (a startup invariant enforced in
// main.go, not something every test process satisfies), so this recovers
// from that panic the same way every other notification-delivery failure in
// this codebase is treated: logged, never surfacing to the caller, because
// the review decision itself already succeeded in the database by the time
// this runs.
func notifyProfileReviewDecision(ctx context.Context, userID string, approved bool, reason string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("failed to deliver profile review notification", "user_id", userID, "recovered", r)
		}
	}()
	err := notifications.NewDefaultService().NotifyProfileReviewDecision(ctx, notifications.ProfileReviewDecisionEvent{
		UserID:   userID,
		Approved: approved,
		Reason:   reason,
	})
	if err != nil {
		slog.Error("failed to deliver profile review notification", "user_id", userID, "error", err)
	}
}

type rejectProfileReviewRequest struct {
	Reason string `json:"reason"`
}

func HandleApproveProfileReview(c echo.Context) error {
	userID := c.Param("user_id")
	reviewedBy := contextutil.GetUserID(c)

	if err := profileapproval.Approve(c.Request().Context(), userID, reviewedBy); err != nil {
		if errors.Is(err, profileapproval.ErrUserNotFound) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		if errors.Is(err, profileapproval.ErrNotPending) {
			return responses.SendError(c, http.StatusConflict, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to approve profile")
	}

	notifyProfileReviewDecision(c.Request().Context(), userID, true, "")
	return responses.SendOK(c, nil, "Profile approved")
}

func HandleRejectProfileReview(c echo.Context) error {
	userID := c.Param("user_id")
	reviewedBy := contextutil.GetUserID(c)

	var req rejectProfileReviewRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}

	// Fast-fail at the HTTP layer too (defense in depth) — the domain layer
	// in profileapproval.Reject still checks this via ErrReasonRequired below.
	if strings.TrimSpace(req.Reason) == "" {
		return responses.SendError(c, http.StatusBadRequest, "a rejection reason is required")
	}

	if err := profileapproval.Reject(c.Request().Context(), userID, reviewedBy, req.Reason); err != nil {
		if errors.Is(err, profileapproval.ErrUserNotFound) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		if errors.Is(err, profileapproval.ErrReasonRequired) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, profileapproval.ErrNotPending) {
			return responses.SendError(c, http.StatusConflict, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to reject profile")
	}

	notifyProfileReviewDecision(c.Request().Context(), userID, false, req.Reason)
	return responses.SendOK(c, nil, "Profile rejected")
}
