package main

import (
	"testing"

	"backend/users"
)

func TestComputeStatus(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		want     string
	}{
		{name: "complete profile backfills to approved", complete: true, want: users.ProfileStatusApproved},
		{name: "incomplete profile backfills to not_submitted", complete: false, want: users.ProfileStatusNotSubmitted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeStatus(tt.complete); got != tt.want {
				t.Errorf("ComputeStatus(%v) = %q, want %q", tt.complete, got, tt.want)
			}
		})
	}
}
