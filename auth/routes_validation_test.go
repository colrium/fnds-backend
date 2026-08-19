package auth

import "testing"

func TestValidateRegistrationRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		request RegisterRequest
		want    string
	}{
		{
			name: "names can be collected after registration",
			request: RegisterRequest{
				Phone:    "+254712345678",
				Password: "Password1!",
				UserType: "artisan",
			},
		},
		{
			name: "phone remains required",
			request: RegisterRequest{
				Password: "Password1!",
				UserType: "artisan",
			},
			want: "phone and password are required",
		},
		{
			name: "password remains required",
			request: RegisterRequest{
				Phone:    "+254712345678",
				UserType: "artisan",
			},
			want: "phone and password are required",
		},
		{
			name: "user type remains required",
			request: RegisterRequest{
				Phone:    "+254712345678",
				Password: "Password1!",
			},
			want: "user_type is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateRegistrationRequiredFields(tt.request); got != tt.want {
				t.Fatalf("validateRegistrationRequiredFields() = %q, want %q", got, tt.want)
			}
		})
	}
}
