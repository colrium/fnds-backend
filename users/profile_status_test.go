package users

import "testing"

func TestEffectiveProfileStatusDefaultsEmptyToNotSubmitted(t *testing.T) {
	u := &User{ProfileStatus: ""}
	if got := EffectiveProfileStatus(u); got != ProfileStatusNotSubmitted {
		t.Errorf("EffectiveProfileStatus() = %q, want %q", got, ProfileStatusNotSubmitted)
	}
}

func TestEffectiveProfileStatusPassesThroughSetValue(t *testing.T) {
	u := &User{ProfileStatus: ProfileStatusApproved}
	if got := EffectiveProfileStatus(u); got != ProfileStatusApproved {
		t.Errorf("EffectiveProfileStatus() = %q, want %q", got, ProfileStatusApproved)
	}
}
