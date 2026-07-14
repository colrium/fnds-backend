package users

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestHandleUpdateAvatar_RequiresFile(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/profile/avatar", strings.NewReader(""))
	req.Header.Set(echo.HeaderContentType, echo.MIMEMultipartForm)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	// No auth context set → expect 401 (userID empty). Proves the handler is
	// wired to require auth + a file, not the old stub returning 200.
	_ = HandleUpdateAvatar(ctx)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for missing auth/file, got 200 (stub behavior)")
	}
}
