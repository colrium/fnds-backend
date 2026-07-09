package auth

import (
	"backend/jwtutil"
	"backend/users"
	"time"
)

func GenerateToken(user *users.User) (string, time.Time, error) {
	return jwtutil.GenerateToken(user.ID)
}

func ValidateToken(tokenString string) (*jwtutil.Claims, error) {
	return jwtutil.ValidateToken(tokenString)
}
