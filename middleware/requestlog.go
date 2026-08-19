package middleware

import (
	"log/slog"
	"net/http"
	"time"

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

// AccessLog emits exactly one structured Log_Record per request, through the
// same slog pipeline every other call site uses, so completed requests are
// visible in Better Stack and carry the request/user/workspace correlation that
// RequestContext, contextutil.SetUser, and contextutil.SetWorkspaceID attach to
// the context.
//
// It replaces echo's built-in Logger() middleware, which writes its own text
// line straight to stdout, bypassing slog entirely — invisible to the
// Better_Stack_Handler and uncorrelated with request.id.
//
// Register after RequestContext() (and after JWTAuth on authenticated groups,
// so user.id is already on the context) so the emitted record carries both.
// The Better_Stack_Handler reads request/user/workspace straight off ctx
// regardless of attrs (Req 4.5, 4.6, 16.11), so Log_Events shipped to Better
// Stack are already correlated by ctx alone. logging.FromContext additionally
// binds the same request/user/workspace groups onto the *logger* used here, so
// the plain slog.JSONHandler writing to stdout carries them too — otherwise
// console output would show a request completed with no way to tie it to the
// rest of that request's Log_Records.
//
// The level split keeps ordinary traffic at INFO and error responses at WARN
// without a second call site: a 5xx status is the server's own fault and
// worth flagging above normal traffic, a 4xx is routine (bad input, expired
// token) and stays at INFO alongside 2xx/3xx.
func AccessLog() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)

			ctx := c.Request().Context()
			status := c.Response().Status
			if err != nil {
				if httpErr, ok := err.(*echo.HTTPError); ok && httpErr.Code != 0 {
					status = httpErr.Code
				} else if status < http.StatusBadRequest {
					status = http.StatusInternalServerError
				}
			}

			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelWarn
			}

			attrs := []slog.Attr{
				slog.String("method", c.Request().Method),
				slog.Int("status", status),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote_ip", c.RealIP()),
			}

			// Emit workspace.id when the request resolved a workspace
			// (Req 16.11). Public routes and routes that have not yet resolved
			// a workspace carry no workspace identifier on the context, so
			// their access log records omit it.
			if wsID, ok := logging.WorkspaceIDFromContext(ctx); ok {
				attrs = append(attrs, slog.Group("workspace", slog.String("id", wsID)))
			}

			logging.FromContext(ctx).LogAttrs(ctx, level, "request completed", attrs...)

			return err
		}
	}
}
