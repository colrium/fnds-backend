package onboarding

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestAuthenticationStateFilter(t *testing.T) {
	tests := []struct {
		name            string
		memberID        string
		normalizedEmail string
		want            bson.M
	}{
		{
			// A state row first created by an email-only authentication has no
			// stytch_member_id, so matching on the member ID alone would miss
			// it and upsert a duplicate (Req 3.6 resumability).
			name:            "member ID and email match either key",
			memberID:        "member-123",
			normalizedEmail: "employer@example.com",
			want: bson.M{"$or": []bson.M{
				{"stytch_member_id": "member-123"},
				{"email": "employer@example.com"},
			}},
		},
		{
			name:     "member ID without email uses member ID",
			memberID: "member-123",
			want:     bson.M{"stytch_member_id": "member-123"},
		},
		{
			name:            "new employer without member ID uses email",
			normalizedEmail: "new@example.com",
			want:            bson.M{"email": "new@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authenticationStateFilter(tt.memberID, tt.normalizedEmail)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("authenticationStateFilter() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
