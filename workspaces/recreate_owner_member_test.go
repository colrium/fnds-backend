package workspaces

import (
	"testing"

	"backend/stytch"
)

// RecreateOwnerMember returns every failure so the webhook is retried, which
// means a failure after CreateMember succeeded runs it again. It searches the
// organization first so that second run adopts the member it already created
// instead of adding another one.
func TestPickReplacementMemberID(t *testing.T) {
	tests := []struct {
		name            string
		existing        []stytch.Member
		deletedMemberID string
		want            string
	}{
		{
			name:            "no members means one must be created",
			deletedMemberID: "member-dead",
			want:            "",
		},
		{
			name:            "adopts a member created by an earlier attempt",
			existing:        []stytch.Member{{MemberID: "member-new"}},
			deletedMemberID: "member-dead",
			want:            "member-new",
		},
		{
			name:            "skips the member the webhook reported deleted",
			existing:        []stytch.Member{{MemberID: "member-dead"}},
			deletedMemberID: "member-dead",
			want:            "",
		},
		{
			name:            "skips the deleted member and adopts the live one",
			existing:        []stytch.Member{{MemberID: "member-dead"}, {MemberID: "member-new"}},
			deletedMemberID: "member-dead",
			want:            "member-new",
		},
		{
			name:            "ignores blank member IDs",
			existing:        []stytch.Member{{MemberID: ""}, {MemberID: "member-new"}},
			deletedMemberID: "member-dead",
			want:            "member-new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickReplacementMemberID(tt.existing, tt.deletedMemberID); got != tt.want {
				t.Fatalf("pickReplacementMemberID() = %q, want %q", got, tt.want)
			}
		})
	}
}
