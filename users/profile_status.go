package users

import "context"

// RevertProfileStatusToPendingIfApproved is called after every successful
// profile edit (see profiles.HandleUpdateProfile). An admin's prior approval
// covered a specific snapshot of profile content; once that content changes,
// the approval no longer speaks to what is now on file, so it must be
// re-checked. It is a no-op for every other status: pending_review has
// nothing to revert from, rejected requires the artisan to explicitly
// resubmit (see profileapproval.SubmitForReview) rather than being silently
// re-queued on every edit.
func RevertProfileStatusToPendingIfApproved(ctx context.Context, user *User) error {
	if user == nil || EffectiveProfileStatus(user) != ProfileStatusApproved {
		return nil
	}
	return UpdateUser(user.ID, map[string]interface{}{
		"profile_status": ProfileStatusPendingReview,
	})
}
