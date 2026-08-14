// Package profileapproval implements the standalone admin approval gate on
// an artisan's profile: pending_review -> approved | rejected. It is
// deliberately independent of the assessments.Journey pipeline (see
// backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md
// section 1 for why the two cannot share state) and does not call
// notifications itself — see the file-level comment in this plan's Task 4
// header for why that responsibility stays in the HTTP handler layer.
package profileapproval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/profiles"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
)

var (
	ErrProfileIncomplete = errors.New("complete your profile before submitting it for review")
	ErrAlreadyPending    = errors.New("your profile is already pending review")
	ErrNotPending        = errors.New("this profile is not currently pending review")
	ErrReasonRequired    = errors.New("a rejection reason is required")
)

// SubmitForReview moves an artisan's profile into pending_review. The
// profile must be complete (the same completeness gate assessments.StartJourney
// and jobrequests' job-application handler already use, so "complete" means
// one thing across the product) and must not already be pending.
func SubmitForReview(ctx context.Context, userID string) error {
	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if users.EffectiveProfileStatus(user) == users.ProfileStatusPendingReview {
		return ErrAlreadyPending
	}

	profile, err := profiles.GetFullProfile(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading profile: %w", err)
	}
	if !profiles.GetProfileCompletion(profile).Complete {
		return ErrProfileIncomplete
	}

	now := time.Now().UTC()
	return users.UpdateUser(userID, bson.M{
		"profile_status":        users.ProfileStatusPendingReview,
		"profile_submitted_at":  now,
		"profile_reject_reason": "",
	})
}

// Approve moves a pending_review profile to approved. reviewedBy is the
// acting admin's user ID.
func Approve(ctx context.Context, userID, reviewedBy string) error {
	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if users.EffectiveProfileStatus(user) != users.ProfileStatusPendingReview {
		return ErrNotPending
	}

	now := time.Now().UTC()
	return users.UpdateUser(userID, bson.M{
		"profile_status":      users.ProfileStatusApproved,
		"profile_reviewed_at": now,
		"profile_reviewed_by": reviewedBy,
	})
}

// Reject moves a pending_review profile to rejected, recording reason so
// the artisan knows what to fix. The artisan may edit their profile and call
// SubmitForReview again — rejection is not final.
func Reject(ctx context.Context, userID, reviewedBy, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrReasonRequired
	}

	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if users.EffectiveProfileStatus(user) != users.ProfileStatusPendingReview {
		return ErrNotPending
	}

	now := time.Now().UTC()
	return users.UpdateUser(userID, bson.M{
		"profile_status":        users.ProfileStatusRejected,
		"profile_reviewed_at":   now,
		"profile_reviewed_by":   reviewedBy,
		"profile_reject_reason": reason,
	})
}
