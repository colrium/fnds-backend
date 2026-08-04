package middleware

import (
	"log/slog"

	"backend/logging"

	"github.com/labstack/echo/v4"
)

// UnmatchedRoutePath is the single fixed placeholder written as the route
// template when an inbound request matches no registered route (Req 4.9).
//
// It is exported so that tests and any consumer of request.path compare against
// this constant rather than duplicating the literal.
const UnmatchedRoutePath = "<unmatched>"

// maxLoggedRejectedIDLen bounds how much of a rejected client-supplied
// X-Request-Id is written to the debug log, so an oversized header cannot
// bloat the log line it is being reported through.
const maxLoggedRejectedIDLen = 200

// RequestContext assigns exactly one request identifier to every inbound
// request, publishes it on the X-Request-Id response header, and attaches the
// identifier, the HTTP method, and the matched route template to the request
// context so that every Log_Record emitted with that context is correlated
// (Req 4.1, 4.2, 4.3, 4.4, 4.8, 4.9).
//
// Register with app.Use, never with app.Pre.
//
// Echo resolves the route before invoking Use middleware but after invoking Pre
// middleware. Under app.Pre, c.Path() is still empty for every request, so the
// route template required by Req 4.8 would never be populated and every
// Log_Event would report the Req 4.9 unmatched placeholder instead. That
// failure is silent — the middleware still runs, the header is still set, and
// only request.path is quietly wrong — so the constraint is stated here because
// it is invisible at the call site.
//
// Register it before Logger() and Recover() so the response header is present on
// handler responses, on centralized error responses, and on recovered panics.
func RequestContext() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()

			// A client-supplied value is adopted byte-for-byte when it is
			// adoptable (Req 4.3). Otherwise it is discarded silently and
			// replaced: an empty, over-long, or illegally-charactered header
			// never rejects the request or produces an error response
			// (Req 4.4).
			id := req.Header.Get(echo.HeaderXRequestID)
			if !logging.ValidRequestID(id) {
				// Rejecting is silent to the client by design (Req 4.4), but not
				// observable is a bug: without this, a caller whose correlation ID
				// keeps getting swapped out has no way to tell why traces never
				// line up. DEBUG keeps it out of ordinary request-log noise.
				if id != "" {
					logged := id
					if len(logged) > maxLoggedRejectedIDLen {
						logged = logged[:maxLoggedRejectedIDLen]
					}
					slog.Debug("client-supplied X-Request-Id rejected, generating replacement",
						slog.String("rejected_id", logged),
					)
				}
				id = logging.NewRequestID()
			}

			// Set before calling next, not after. Echo commits the response as
			// soon as a handler, the centralized error handler, or the recover
			// middleware writes to it, and headers set after that point are
			// discarded. Setting it here is what makes the header present on
			// every response, including error responses and recovered panics
			// (Req 4.2).
			c.Response().Header().Set(echo.HeaderXRequestID, id)

			// c.Path() is the registered route template with no path parameter
			// substituted, e.g. /api/v1/job-requests/:jobRequestId/chats
			// (Req 4.8). It is empty when no route matched.
			path := c.Path()
			if path == "" {
				path = UnmatchedRoutePath
			}

			c.SetRequest(req.WithContext(
				logging.ContextWithRequest(req.Context(), id, req.Method, path),
			))

			return next(c)
		}
	}
}
