package adminapi

import (
	"strconv"

	"github.com/labstack/echo/v4"
)

const (
	defaultAdminPageLimit = 20
	maxAdminPageLimit     = 100
)

// Page is the shared envelope for every paginated admin list response,
// matching the adminPage[T] shape already used by assessments' admin routes.
type Page[T any] struct {
	Data  []T   `json:"data"`
	Total int64 `json:"total"`
	Page  int64 `json:"page"`
	Limit int64 `json:"limit"`
}

func parsePageLimit(c echo.Context) (page, limit int64) {
	page, limit = 1, defaultAdminPageLimit
	if p := c.QueryParam("page"); p != "" {
		if v, err := strconv.ParseInt(p, 10, 64); err == nil && v > 0 {
			page = v
		}
	}
	if l := c.QueryParam("limit"); l != "" {
		if v, err := strconv.ParseInt(l, 10, 64); err == nil && v > 0 && v <= maxAdminPageLimit {
			limit = v
		}
	}
	return page, limit
}
