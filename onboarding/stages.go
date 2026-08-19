package onboarding

// ValidTransition reports whether moving from `from` to `to` is allowed.
// Every transition is a conditional findOneAndUpdate naming the expected current
// stage, so a request in the wrong stage matches zero documents and returns 409.
//
// The valid transitions are:
//   - awaiting_verification -> awaiting_link_decision  (candidates found)
//   - awaiting_verification -> awaiting_workspace_name (no candidates)
//   - awaiting_link_decision -> awaiting_workspace_name (link satisfied or create separate)
//   - awaiting_workspace_name -> provisioning           (name accepted, lease won)
//   - provisioning -> provisioning                      (retry reuses org id)
//   - provisioning -> awaiting_workspace_name           (lease expired)
//   - provisioning -> complete                          (success)
func ValidTransition(from, to Stage) bool {
	switch from {
	case StageAwaitingVerification:
		return to == StageAwaitingLinkDecision || to == StageAwaitingWorkspaceName
	case StageAwaitingLinkDecision:
		return to == StageAwaitingWorkspaceName
	case StageAwaitingWorkspaceName:
		return to == StageProvisioning
	case StageProvisioning:
		return to == StageProvisioning || to == StageAwaitingWorkspaceName || to == StageComplete
	default:
		return false
	}
}
