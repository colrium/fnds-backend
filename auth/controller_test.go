package auth

import (
	"errors"
	"testing"

	"backend/users"
)

func TestAccountLoginError(t *testing.T) {
	tests := []struct {
		name    string
		user    *users.User
		wantErr error
	}{
		{
			name:    "pending account is rejected",
			user:    &users.User{Status: users.StatusPending, IsActive: false},
			wantErr: ErrAccountPending,
		},
		{
			name:    "suspended account is rejected even if flagged active",
			user:    &users.User{Status: users.StatusSuspended, IsActive: true},
			wantErr: ErrAccountSuspended,
		},
		{
			name:    "active verified account is allowed",
			user:    &users.User{Status: users.StatusActive, IsActive: true},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := accountLoginError(tt.user)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}
