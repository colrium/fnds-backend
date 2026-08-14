package adminapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func newTestContext(rawQuery string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestParsePageLimitDefaults(t *testing.T) {
	page, limit := parsePageLimit(newTestContext(""))
	if page != 1 {
		t.Errorf("page = %d, want 1", page)
	}
	if limit != defaultAdminPageLimit {
		t.Errorf("limit = %d, want %d", limit, defaultAdminPageLimit)
	}
}

func TestParsePageLimitClampsOversizedLimit(t *testing.T) {
	_, limit := parsePageLimit(newTestContext("limit=99999"))
	if limit != defaultAdminPageLimit {
		t.Errorf("limit = %d, want default %d when oversized", limit, defaultAdminPageLimit)
	}
}

func TestParsePageLimitRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"zero page", "page=0"},
		{"negative page", "page=-1"},
		{"garbage page", "page=abc"},
		{"garbage limit", "limit=abc"},
		{"negative limit", "limit=-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, limit := parsePageLimit(newTestContext(tt.query))
			if page != 1 {
				t.Errorf("page = %d, want default 1", page)
			}
			if limit != defaultAdminPageLimit {
				t.Errorf("limit = %d, want default %d", limit, defaultAdminPageLimit)
			}
		})
	}
}

func TestParsePageLimitAcceptsValidValues(t *testing.T) {
	page, limit := parsePageLimit(newTestContext("page=3&limit=50"))
	if page != 3 {
		t.Errorf("page = %d, want 3", page)
	}
	if limit != 50 {
		t.Errorf("limit = %d, want 50", limit)
	}
}
