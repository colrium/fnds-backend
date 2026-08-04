package contextutil

import (
	"github.com/labstack/echo/v4"

	"backend/logging"
)

// SetUser records the authenticated user identifier for the current request in
// two places.
//
// The echo store entry is what GetUserID reads, and it is unchanged, so every
// existing caller keeps working. The request context entry is what the
// Better_Stack_Handler reads: slog.Handler.Handle receives a context.Context,
// not an echo.Context, so echo's c.Set store is invisible to it. Writing the id
// into the stdlib context is what makes user.id appear on Log_Events emitted
// downstream of authentication (Req 4.6). Public routes never call SetUser, so
// their Log_Events carry no user object (Req 4.10).
//
// logging imports the standard library only — in particular never echo — so
// this contextutil -> logging edge creates no import cycle.
func SetUser(c echo.Context, userID string) {
	c.Set("user_id", userID)

	// A nil request is not expected from echo's own routing, but SetUser is also
	// called directly from tests and handlers, and logging must never be the
	// reason a request fails.
	r := c.Request()
	if r == nil {
		return
	}
	c.SetRequest(r.WithContext(logging.ContextWithUserID(r.Context(), userID)))
}

func GetUserID(c echo.Context) string {
	id, ok := c.Get("user_id").(string)
	if !ok {
		return ""
	}
	return id
}
