package workspaces

import (
	"testing"

	"backend/onboarding"
)

// Every name this returns is fed straight into CreateForOnboarding, which calls
// ValidateName. A derivation that produced a name shorter than 2 characters
// would reject a sign-up over a name the employer never chose, so the property
// that actually matters is that the result always validates.

func TestDeriveDefaultWorkspaceName(t *testing.T) {
	cases := []struct {
		name  string
		state onboarding.OnboardingState
		want  string
	}{
		{
			name:  "prefers the profile name",
			state: onboarding.OnboardingState{FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com"},
			want:  "Ada Lovelace",
		},
		{
			name:  "first name alone is enough",
			state: onboarding.OnboardingState{FirstName: "Ada", Email: "ada@example.com"},
			want:  "Ada",
		},
		{
			name:  "falls back to the email local part",
			state: onboarding.OnboardingState{Email: "employer@example.com"},
			want:  "employer",
		},
		{
			// A one-character profile name fails ValidateName, so it must not be
			// preferred over a usable email local part.
			name:  "too-short profile name loses to the email",
			state: onboarding.OnboardingState{FirstName: "A", Email: "employer@example.com"},
			want:  "employer",
		},
		{
			// "a@x.com" — the local part is itself too short to validate.
			name:  "too-short local part falls through to the placeholder",
			state: onboarding.OnboardingState{Email: "a@example.com"},
			want:  "Workspace",
		},
		{
			name:  "no name and no usable email still yields a valid name",
			state: onboarding.OnboardingState{},
			want:  "Workspace",
		},
		{
			name:  "whitespace-padded profile name is trimmed",
			state: onboarding.OnboardingState{FirstName: "  Ada  ", LastName: "  Lovelace  ", Email: "ada@example.com"},
			want:  "Ada     Lovelace",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := deriveDefaultWorkspaceName(&testCase.state)
			if got != testCase.want {
				t.Errorf("deriveDefaultWorkspaceName() = %q, want %q", got, testCase.want)
			}
			// The invariant, independent of the exact string above.
			if err := ValidateName(got); err != nil {
				t.Errorf("derived name %q does not satisfy ValidateName: %v", got, err)
			}
		})
	}
}

// completeWithDefaultWorkspace must be inert at every other stage, or an
// employer mid-verification would have a workspace provisioned underneath them.
func TestCompleteWithDefaultWorkspaceOnlyHandlesTheNamingStage(t *testing.T) {
	otherStages := []onboarding.Stage{
		onboarding.StageAwaitingVerification,
		onboarding.StageAwaitingLinkDecision,
		onboarding.StageProvisioning,
		onboarding.StageComplete,
	}

	for _, stage := range otherStages {
		t.Run(string(stage), func(t *testing.T) {
			// A nil OnboardingService would panic if the stage check did not
			// short-circuit first, which is the point of passing one.
			handled, err := completeWithDefaultWorkspace(nil, RouteConfig{}, "state-1", string(stage))
			if handled {
				t.Errorf("stage %q was handled; only awaiting_workspace_name may be", stage)
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
