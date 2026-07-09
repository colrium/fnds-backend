package auth

import (
	"fmt"

	"backend/users"

	"golang.org/x/crypto/bcrypt"
)

func processUserLogin(email, phone, password, userType string) (*users.User, string, error) {
	var user *users.User
	var err error

	if email != "" {
		user, err = users.GetUserByEmail(email)
	} else if phone != "" {
		user, err = users.GetUserByPhone(phone)
	} else {
		return nil, "", fmt.Errorf("email or phone required")
	}

	if err != nil {
		return nil, "", fmt.Errorf("invalid credentials")
	}

	if user.UserType != userType {
		return nil, "", fmt.Errorf("invalid credentials")
	}

	if !user.IsActive {
		return nil, "", fmt.Errorf("account suspended")
	}

	err = bcrypt.CompareHashAndPassword(user.Password, []byte(password))
	if err != nil {
		return nil, "", fmt.Errorf("invalid credentials")
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		return nil, "", err
	}

	return user, token, nil
}

func createNewUser(firstName, lastName, email, phone, password, userType string) (*users.User, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return nil, err
	}

	otp := "123456"

	user := &users.User{
		FirstName:     firstName,
		LastName:      lastName,
		Email:         email,
		Phone:         phone,
		Password:      hashedPassword,
		UserType:      userType,
		Status:        "active",
		IsActive:      true,
		Otp:           otp,
		PhoneVerified: false,
	}

	err = users.CreateNewUser(user)
	if err != nil {
		return nil, err
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
		Status:        "active",
		IsActive:      true,
		PhoneVerified: true,
	}

	err = users.CreateNewUser(user)
	if err != nil {
		return nil, err
	}
	return user, nil
}
