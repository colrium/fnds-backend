package middleware

import (
	"net/http"
	"strings"

	"backend/contextutil"
	"backend/jwtutil"
	"backend/responses"
	"backend/users"

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
		claims, err := jwtutil.ValidateToken(tokenString)
		if err != nil {
			return responses.SendError(c, http.StatusUnauthorized, "Invalid or expired token")
		}

		user, err := users.GetUserByID(c.Request().Context(), claims.UserID)
		if err != nil || user == nil {
			return responses.SendError(c, http.StatusUnauthorized, "User not found")
		}

		contextutil.SetUser(c, user.ID)
		return next(c)
	}
}
