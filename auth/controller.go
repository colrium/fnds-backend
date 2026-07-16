package auth

import (
	"errors"
	"log"

	"backend/notifications"
	"backend/users"

	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountPending     = errors.New("account pending phone verification")
	ErrAccountSuspended   = errors.New("account suspended")
	ErrDuplicateUser      = errors.New("an account with this phone or email already exists for this user type")
)

func processUserLogin(email, phone, password string) (*users.User, string, error) {
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

func createNewUser(firstName, lastName, email, phone, password, userType string) (*users.User, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return nil, err
	}

	otp := users.GenerateOTP()

	user := &users.User{
		FirstName:     firstName,
		LastName:      lastName,
		Email:         email,
		Phone:         phone,
		Password:      hashedPassword,
		UserType:      userType,
		Status:        users.StatusPending,
		IsActive:      false,
		Otp:           otp,
		PhoneVerified: false,
	}

	err = users.CreateNewUser(user)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrDuplicateUser
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
			return nil, ErrDuplicateUser
		}
		return nil, err
	}
	return user, nil
}
