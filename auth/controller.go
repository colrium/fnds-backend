package auth

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"backend/notifications"
	"backend/users"
	"backend/workspaces"

	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrAccountPending        = errors.New("account pending phone verification")
	ErrAccountSuspended      = errors.New("account suspended")
	ErrDuplicateUser         = errors.New("an account with this phone or email already exists for this user type")
	ErrDuplicatePhone        = errors.New("an account with this phone number already exists")
	ErrDuplicateEmail        = errors.New("an account with this email already exists")
	ErrPasswordResetRequired = errors.New("password reset required")
	ErrStytchSignInRequired  = errors.New("stytch sign-in required")
)

// employerUserType is the `users.user_type` value an Employer holds. Employers
// are `customer` documents; the Stytch migration touches no other type
// (Req 21.14).
const employerUserType = "customer"

// legacyLoginBlockedByCutover reports whether this user's legacy password login
// must be refused because employer auth has cut over to Stytch.
//
// Req 21.11 refuses legacy login for a migrated Employer once
// LEGACY_EMPLOYER_AUTH_ENABLED is off. Req 21.12 requires the reverse — while
// that variable is on, a migrated Employer's old sign-in keeps working, and a
// revert is an env flip alone — so the flag is checked first and no
// account_links read happens at all in the pre-cutover state.
//
// Req 22.2 keeps every `users` document holding zero Account_Links on the legacy
// path. Only `customer` documents ever hold one (Req 21.14), so an artisan,
// assessor, or admin login never reaches the account_links read either.
func legacyLoginBlockedByCutover(ctx context.Context, user *users.User) bool {
	if workspaces.IsLegacyAuthEnabled() {
		return false
	}
	if user.UserType != employerUserType {
		return false
	}
	return workspaces.IsUserMigrated(ctx, user.ID)
}

func processUserLogin(ctx context.Context, email, phone, password string) (*users.User, string, error) {
	var user *users.User
	var err error

	if email != "" {
		user, err = users.GetUserByEmail(email)
	} else if phone != "" {
		user, err = users.GetUserByPhone(phone)
	} else {
		return nil, "", ErrInvalidCredentials
	}

	if err != nil {
		return nil, "", ErrInvalidCredentials
	}

	// Checked before the password and before the legacy-platform reset steer: a
	// migrated Employer must land on Stytch sign-in, not be handed a legacy
	// session or an OTP reset that ends in one (Req 21.11).
	if legacyLoginBlockedByCutover(ctx, user) {
		return nil, "", ErrStytchSignInRequired
	}

	// Users imported from the old platform have no usable password (their hashes
	// were not migrated). Steer them into the reset flow instead of returning a
	// confusing "invalid credentials".
	if user.Migrated {
		return user, "", ErrPasswordResetRequired
	}

	if err := bcrypt.CompareHashAndPassword(user.Password, []byte(password)); err != nil {
		return nil, "", ErrInvalidCredentials
	}

	if err := accountLoginError(user); err != nil {
		return nil, "", err
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		return nil, "", err
	}

	return user, token, nil
}

// accountLoginError reports why an authenticated user still can't log in, checked
// only after the password has already been verified so status is never leaked
// to an unauthenticated caller.
func accountLoginError(user *users.User) error {
	if user.Status == users.StatusSuspended {
		return ErrAccountSuspended
	}
	if !user.IsActive {
		return ErrAccountPending
	}
	return nil
}

func createNewUser(
	firstName, lastName, email, phone, password, userType, referralCode string,
) (*users.User, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return nil, err
	}

	otp := users.GenerateOTP()

	user := &users.User{
		FirstName:        firstName,
		LastName:         lastName,
		Email:            email,
		Phone:            phone,
		Password:         hashedPassword,
		UserType:         userType,
		Status:           users.StatusPending,
		IsActive:         false,
		Otp:              otp,
		OtpExpiresAt:     time.Now().UTC().Add(users.OTPTTL),
		PhoneVerified:    false,
		ReferralCodeUsed: referralCode,
	}

	err = users.CreateNewUser(user)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			classified := classifyDuplicateKeyError(err)
			notifyDuplicateRegistrationAttempt(classified, email, phone)
			return nil, classified
		}
		return nil, err
	}

	// Send the verification code; a delivery failure shouldn't fail registration
	// since the user can resend from the verify screen.
	if err := notifications.SendOTPSMS(user.Phone, otp); err != nil {
		log.Printf("[register] failed to send OTP SMS to %s: %v", user.Phone, err)
	}
	return user, nil
}

// notifyDuplicateRegistrationAttempt alerts the existing account holder that
// someone tried to sign up with their email. Fire-and-forget: never block
// the registration response, never leak whether the account exists.
func notifyDuplicateRegistrationAttempt(dupErr error, attemptedEmail, attemptedPhone string) {
	if !errors.Is(dupErr, ErrDuplicateEmail) || attemptedEmail == "" {
		return
	}
	go func(email string) {
		existing, err := users.GetUserByEmail(email)
		if err != nil || existing == nil {
			return
		}
		name := strings.TrimSpace(existing.FirstName + " " + existing.LastName)
		if err := notifications.SendDuplicateRegistrationAlertEmail(existing.Email, name); err != nil {
			log.Printf("[register] failed to send duplicate-registration alert to %s: %v", existing.Email, err)
		}
	}(attemptedEmail)
	_ = attemptedPhone
}

func createUserViaAdmin(firstName, lastName, email, phone, password, userType string) (*users.User, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return nil, err
	}

	user := &users.User{
		FirstName:     firstName,
		LastName:      lastName,
		Email:         email,
		Phone:         phone,
		Password:      hashedPassword,
		UserType:      userType,
		Status:        users.StatusActive,
		IsActive:      true,
		PhoneVerified: true,
	}

	err = users.CreateNewUser(user)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			classified := classifyDuplicateKeyError(err)
			notifyDuplicateRegistrationAttempt(classified, email, phone)
			return nil, classified
		}
		return nil, err
	}
	return user, nil
}

// classifyDuplicateKeyError inspects a mongo duplicate-key error and returns a
// field-specific sentinel (phone/email) when we can figure it out, otherwise
// falls back to the generic ErrDuplicateUser.
func classifyDuplicateKeyError(err error) error {
	var writeException mongo.WriteException
	if errors.As(err, &writeException) {
		for _, we := range writeException.WriteErrors {
			msg := strings.ToLower(we.Message)
			if strings.Contains(msg, "phone") {
				return ErrDuplicatePhone
			}
			if strings.Contains(msg, "email") {
				return ErrDuplicateEmail
			}
		}
	}
	return ErrDuplicateUser
}
