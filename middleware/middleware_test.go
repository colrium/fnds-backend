package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func TestGetUserIDReturnsEmptyWhenNotSet(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	id := contextutil.GetUserID(c)
	if id != "" {
		t.Error("expected empty user ID when not set in context")
	}
}

func TestIsAdminRejectsNonAdmin(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := IsAdmin(func(c echo.Context) error {
		return c.JSON(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestIsArtisanRejectsNonArtisan(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := IsArtisan(func(c echo.Context) error {
		return c.JSON(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestIsCustomerRejectsNonCustomer(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := IsCustomer(func(c echo.Context) error {
		return c.JSON(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestIsAssessorRejectsNonAssessor(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := IsAssessor(func(c echo.Context) error {
		return c.JSON(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestIsSuspendedRejectsInactiveUser(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := IsSuspended(func(c echo.Context) error {
		return c.JSON(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestResponsesSendOK(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := responses.SendOK(c, map[string]string{"key": "value"}, "success")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestResponsesSendError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := responses.SendError(c, http.StatusBadRequest, "bad request")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestResponsesSendCreated(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := responses.SendCreated(c, nil, "created")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}
}
