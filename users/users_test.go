package users

import (
	"testing"
)

func TestGenerateOTP(t *testing.T) {
	otp := GenerateOTP()
	if len(otp) != 6 {
		t.Errorf("GenerateOTP() = %q, want a 6-character code", otp)
	}
	for _, c := range otp {
		if c < '0' || c > '9' {
			t.Errorf("GenerateOTP() = %q, want digits only", otp)
			break
		}
	}
	// Two calls should almost never collide; a repeat signals a non-random source.
	secondOTP := GenerateOTP()
	thirdOTP := GenerateOTP()
	if secondOTP == otp && thirdOTP == otp {
		t.Errorf("GenerateOTP() appears non-random: repeated %q", otp)
	}
}

func TestResetPasswordValidation(t *testing.T) {
	tests := []struct {
		name            string
		phone           string
		otp             string
		password        string
		confirmPassword string
		userType        string
		wantErr         bool
		errMsg          string
	}{
		{
			name:            "missing fields",
			phone:           "",
			otp:             "",
			password:        "",
			confirmPassword: "",
			userType:        "",
			wantErr:         true,
			errMsg:          "all fields are required",
		},
		{
			name:            "password mismatch",
			phone:           "1234567890",
			otp:             "123456",
			password:        "password1",
			confirmPassword: "password2",
			userType:        "artisan",
			wantErr:         true,
			errMsg:          "passwords do not match",
		},
		{
			name:            "short password",
			phone:           "1234567890",
			otp:             "123456",
			password:        "short",
			confirmPassword: "short",
			userType:        "artisan",
			wantErr:         true,
			errMsg:          "password must be at least 8 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ResetPassword(tt.phone, tt.otp, tt.password, tt.confirmPassword, tt.userType)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestForgotPasswordValidation(t *testing.T) {
	tests := []struct {
		name     string
		phone    string
		userType string
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "missing phone",
			phone:    "",
			userType: "artisan",
			wantErr:  true,
			errMsg:   "phone and user_type are required",
		},
		{
			name:     "missing user_type",
			phone:    "1234567890",
			userType: "",
			wantErr:  true,
			errMsg:   "phone and user_type are required",
		},
		{
			name:     "missing both",
			phone:    "",
			userType: "",
			wantErr:  true,
			errMsg:   "phone and user_type are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ForgotPassword(tt.phone, tt.userType)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpdateFCMTokenValidation(t *testing.T) {
	err := UpdateFCMToken("user123", "")
	if err == nil {
		t.Error("expected error for empty token, got nil")
	}
}
