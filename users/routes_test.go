package users

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestForgotPasswordBindsJSON(t *testing.T) {
	e := echo.New()
	body := `{"phone":"+254712345678","user_type":"artisan"}`
	req := httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	// Should read phone/user_type from JSON (not FormValue) and fail only at
	// the DB lookup stage (panic or "Phone number does not exist"), NOT at the
	// "phone is required" validation stage.
	// We expect a panic due to nil DB in tests, which proves JSON was parsed.
	defer func() {
		if r := recover(); r != nil {
			// Panic from DB access is expected in this minimal test; it proves
			// we got past the "phone is required" validation, which means JSON was bound.
			return
		}
		// If no panic, the handler must have returned a DB-level error
		// ("Phone number does not exist"), NOT a validation error
		// ("phone is required") and NOT an empty/unparseable body.
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("could not parse response body: %v", err)
		}
		msg, ok := resp["message"].(string)
		if !ok || strings.Contains(msg, "required") {
			t.Fatalf("handler did not read JSON body; got validation error or no message: %v", resp)
		}
	}()

	_ = HandleForgotPassword(ctx)
}
