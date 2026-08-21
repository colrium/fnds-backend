package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
)

func newCORSMiddleware(appEnv string) echo.MiddlewareFunc {
	allowLocalOrigins := allowsLocalOrigins(appEnv)

	return echomiddleware.CORSWithConfig(echomiddleware.CORSConfig{
		AllowOriginFunc: func(origin string) (bool, error) {
			return isFundisOrigin(origin) ||
				(allowLocalOrigins && isLocalDevelopmentOrigin(origin)), nil
		},
		AllowMethods: []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodPut,
			http.MethodPatch,
			http.MethodPost,
			http.MethodDelete,
		},
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
		},
		MaxAge: 86400,
	})
}

// allowsLocalOrigins reports whether a locally-run frontend may call this
// environment. Staging is included so the admin dashboard can be developed on
// localhost against staging data without needing a local backend.
//
// This is an explicit allowlist rather than `appEnv != "production"` so that an
// unset or misspelled APP_ENV fails closed: APP_ENV comes straight from the
// environment with no default (see configs.AppEnv), and a deploy that loses it
// must not silently start accepting localhost origins.
func allowsLocalOrigins(appEnv string) bool {
	switch appEnv {
	case "development", "staging", "test":
		return true
	default:
		return false
	}
}

func isFundisOrigin(origin string) bool {
	parsed, ok := parseOrigin(origin)
	if !ok || parsed.Scheme != "https" {
		return false
	}

	host := strings.ToLower(parsed.Hostname())
	return host == "fundis.co.ke" || strings.HasSuffix(host, ".fundis.co.ke")
}

func isLocalDevelopmentOrigin(origin string) bool {
	parsed, ok := parseOrigin(origin)
	if !ok || parsed.Scheme != "http" {
		return false
	}

	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1"
}

func parseOrigin(origin string) (*url.URL, bool) {
	parsed, err := url.Parse(origin)
	if err != nil ||
		parsed.Scheme == "" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, false
	}
	return parsed, true
}
