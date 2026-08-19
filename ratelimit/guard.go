package ratelimit

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Guard returns an Echo middleware that applies the named rate limit.
// The subject function extracts the rate-limit key from the request context
// (e.g. IP address, email, workspace ID).
//
// On rejection:
//   - Sets Retry-After header naming the remaining seconds (Req 25.3)
//   - Emits one warning-severity log naming the endpoint, limit, and IP (Req 25.8)
//   - Returns 429 with a JSON error body
func (l *Limiter) Guard(n Name, subject func(echo.Context) string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			subj := subject(c)
			decision := l.Allow(c.Request().Context(), n, subj)
			if !decision.Allowed {
				// Set Retry-After header (Req 25.3)
				retryAfter := int(decision.RetryAfter.Seconds())
				if retryAfter <= 0 {
					retryAfter = 1
				}
				c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))

				// Emit warning log (Req 25.8)
				slog.Warn("rate limit exceeded",
					"endpoint", c.Path(),
					"limit", string(n),
					"ip", c.RealIP(),
					"subject", subj,
				)

				// Return 429
				return c.JSON(http.StatusTooManyRequests, map[string]any{
					"success": false,
					"message": "Too many requests",
					"code":    string(n) + "_rate_limited",
				})
			}
			return next(c)
		}
	}
}

// SubjectIP extracts the client IP as the rate-limit subject.
func SubjectIP(c echo.Context) string {
	return c.RealIP()
}

// SubjectFormEmail extracts a normalized email from the request form's "email" field.
// This is a convenience for signup endpoints where the email is submitted as a form
// value or query parameter. For JSON-body endpoints, the route handler should extract
// the email and pass it via a custom subject function.
func SubjectFormEmail(c echo.Context) string {
	return c.FormValue("email")
}

// SubjectFromHeader returns a subject extractor that reads from a named request header.
// Use this when the rate-limit key is carried in a header (e.g. an API key or session token).
func SubjectFromHeader(header string) func(echo.Context) string {
	return func(c echo.Context) string {
		return c.Request().Header.Get(header)
	}
}
