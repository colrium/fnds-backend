package middleware

import (
	"net/http"
	"strings"

	"backend/auth"
	"backend/responses"
	"backend/users"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func JWTAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		authHeader := c.Request().Header.Get("Authorization")
		if authHeader == "" {
			return responses.SendError(c, http.StatusUnauthorized, "Authorization header required")
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			return responses.SendError(c, http.StatusUnauthorized, "Invalid authorization header format")
		}

		tokenString := parts[1]
		claims := &auth.Claims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return []byte(auth.GetJWTSecret()), nil
		})

		if err != nil || !token.Valid {
			return responses.SendError(c, http.StatusUnauthorized, "Invalid or expired token")
		}

		user, err := users.GetUserByID(claims.UserID)
		if err != nil || user == nil {
			return responses.SendError(c, http.StatusUnauthorized, "User not found")
		}

		c.Set("user", user)
		c.Set("user_id", user.ID)
		return next(c)
	}
}

func GetCurrentUser(c echo.Context) *users.User {
	user, ok := c.Get("user").(*users.User)
	if !ok {
		return nil
	}
	return user
}

func GetUserID(c echo.Context) string {
	id, ok := c.Get("user_id").(string)
	if !ok {
		return ""
	}
	return id
}
