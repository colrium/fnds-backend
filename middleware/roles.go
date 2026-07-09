package middleware

import (
	"net/http"

	"backend/responses"

	"github.com/labstack/echo/v4"
)

func IsAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := GetCurrentUser(c)
		if user == nil || user.UserType != "admin" {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		return next(c)
	}
}

func IsArtisan(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := GetCurrentUser(c)
		if user == nil || user.UserType != "artisan" {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		return next(c)
	}
}

func IsCustomer(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := GetCurrentUser(c)
		if user == nil || user.UserType != "customer" {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		return next(c)
	}
}

func IsAssessor(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := GetCurrentUser(c)
		if user == nil || user.UserType != "assessor" {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		return next(c)
	}
}

func IsSuspended(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := GetCurrentUser(c)
		if user == nil || !user.IsActive {
			return responses.SendError(c, http.StatusUnauthorized, "Account suspended")
		}
		return next(c)
	}
}

func CheckSubscription(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := GetCurrentUser(c)
		if user == nil {
			return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
		}
		// Subscription check logic will be implemented in subscriptions package
		return next(c)
	}
}
