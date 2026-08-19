package audit

import (
	"net/http"
	"strconv"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
)

// HandleList is the handler for GET /workspaces/:workspaceID/audit-log.
// It requires the audit.view permission (enforced by the ScopedGroup guard)
// and returns entries in reverse chronological order with pagination.
//
// Query parameters:
//   - page: 1-indexed page number (default 1)
//   - limit: entries per page, 1–100 (default 50)
//
// Requirements: 23.3, 23.4, 23.5, 23.6.
func (l *Logger) HandleList(c echo.Context) error {
	workspaceID := contextutil.GetWorkspaceID(c)
	if workspaceID == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"success": false,
			"message": "missing workspace id",
		})
	}

	page, _ := strconv.ParseInt(c.QueryParam("page"), 10, 64)
	limit, _ := strconv.ParseInt(c.QueryParam("limit"), 10, 64)

	result, err := l.List(c.Request().Context(), workspaceID, page, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "failed to retrieve audit log",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"success": true,
		"data":    result,
	})
}
