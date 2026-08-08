package cvparser

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestObjectKeyBelongsToUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		objectKey string
		userID    string
		want      bool
	}{
		{
			name:      "valid ownership",
			objectKey: "users/user-123/abc/file.pdf",
			userID:    "user-123",
			want:      true,
		},
		{
			name:      "different user",
			objectKey: "users/user-456/abc/file.pdf",
			userID:    "user-123",
			want:      false,
		},
		{
			name:      "prefix attack with longer ID",
			objectKey: "users/user-1234/abc/file.pdf",
			userID:    "user-123",
			want:      false,
		},
		{
			name:      "no users prefix",
			objectKey: "other/user-123/file.pdf",
			userID:    "user-123",
			want:      false,
		},
		{
			name:      "empty key",
			objectKey: "",
			userID:    "user-123",
			want:      false,
		},
		{
			name:      "empty user ID",
			objectKey: "users//abc/file.pdf",
			userID:    "",
			want:      true,
		},
		{
			name:      "path traversal attempt",
			objectKey: "users/user-123/../user-456/file.pdf",
			userID:    "user-123",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := objectKeyBelongsToUser(tt.objectKey, tt.userID)
			if got != tt.want {
				t.Fatalf("objectKeyBelongsToUser(%q, %q) = %v, want %v",
					tt.objectKey, tt.userID, got, tt.want)
			}
		})
	}
}

func TestMapParseError(t *testing.T) {
	t.Parallel()

	h := &Handler{}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unreadable PDF",
			err:        ErrPDFUnreadable,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "pdf_unreadable",
		},
		{
			name:       "password-protected PDF",
			err:        ErrPDFPasswordProtected,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "pdf_password_protected",
		},
		{
			name:       "PDF too large",
			err:        ErrPDFTooLarge,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "pdf_too_large",
		},
		{
			name:       "wrapped PDF too large",
			err:        errors.Join(ErrPDFTooLarge, errors.New("size exceeded")),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "pdf_too_large",
		},
		{
			name:       "parse timeout",
			err:        ErrParseTimeout,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "parse_timeout",
		},
		{
			name:       "parser unavailable",
			err:        ErrParserUnavailable,
			wantStatus: http.StatusBadGateway,
			wantCode:   "parser_unavailable",
		},
		{
			name:       "unknown error",
			err:        errors.New("something unexpected"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			rec := httptest.NewRecorder()
			c := app.NewContext(req, rec)

			_ = h.mapParseError(c, tt.err, "user-1", "file-1")

			if rec.Code != tt.wantStatus {
				t.Fatalf("mapParseError() status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantCode != "" && !strings.Contains(rec.Body.String(), tt.wantCode) {
				t.Fatalf("mapParseError() body = %q, want code %q", rec.Body.String(), tt.wantCode)
			}
		})
	}
}
