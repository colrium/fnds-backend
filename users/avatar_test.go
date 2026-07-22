package users

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/contextutil"
	"backend/files"

	"github.com/labstack/echo/v4"
)

type avatarRepositoryStub struct {
	currentURL string
	savedURL   string
	err        error
}

func (r *avatarRepositoryStub) GetAvatarURL(context.Context, string) (string, error) {
	return r.currentURL, r.err
}

func (r *avatarRepositoryStub) SetAvatarURL(_ context.Context, _ string, avatarURL string) error {
	r.savedURL = avatarURL
	return r.err
}

type avatarStorageStub struct {
	object       *files.ObjectInfo
	deletedKey   string
	completedKey string
	err          error
}

func (s *avatarStorageStub) PresignUpload(
	context.Context,
	string,
	string,
	string,
	int64,
) (*files.UploadIntent, error) {
	return &files.UploadIntent{
		UploadURL: "https://bucket.test/upload",
		ObjectKey: "users/user-1/avatars/avatar.png",
		Headers:   map[string]string{"Content-Type": "image/png"},
	}, s.err
}

func (s *avatarStorageStub) PresignDownload(
	context.Context,
	string,
) (string, error) {
	return "https://bucket.test/download?signature=example", s.err
}

func (s *avatarStorageStub) Stat(context.Context, string) (*files.ObjectInfo, error) {
	return s.object, s.err
}

func (s *avatarStorageStub) MarkComplete(_ context.Context, objectKey string) error {
	s.completedKey = objectKey
	return s.err
}

func (s *avatarStorageStub) Delete(_ context.Context, objectKey string) error {
	s.deletedKey = objectKey
	return s.err
}

func (s *avatarStorageStub) PublicURL(objectKey string) string {
	return "https://cdn.test/" + objectKey
}

func (s *avatarStorageStub) KeyFromURL(fileURL string) (string, error) {
	return strings.TrimPrefix(fileURL, "https://cdn.test/"), nil
}

func TestAvatarHandlerCompletePersistsAndReturnsRenderableURL(t *testing.T) {
	t.Setenv("API_PUBLIC_URL", "https://api.test")

	repository := &avatarRepositoryStub{}
	storage := &avatarStorageStub{object: &files.ObjectInfo{
		Size:        6,
		ContentType: "image/png",
		UserID:      "user-1",
	}}
	handler := NewAvatarHandler(repository, storage)
	body := `{
		"object_key":"users/user-1/avatars/avatar.png",
		"mime_type":"image/png",
		"size":6
	}`
	ctx, recorder := newAvatarContext(
		http.MethodPost,
		"/profile/avatar/complete",
		body,
		"user-1",
	)

	if err := handler.Complete(ctx); err != nil {
		t.Fatalf("Complete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("Complete() status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if repository.savedURL != "https://cdn.test/users/user-1/avatars/avatar.png" {
		t.Fatalf("Complete() saved URL = %q", repository.savedURL)
	}
	if storage.completedKey != "users/user-1/avatars/avatar.png" {
		t.Fatalf("Complete() finalized key = %q", storage.completedKey)
	}

	var response struct {
		Data struct {
			Avatar string `json:"avatar"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if response.Data.Avatar != "https://api.test/api/v1/avatars/user-1?v=avatar.png" {
		t.Fatalf("Complete() avatar = %q", response.Data.Avatar)
	}
}

func TestAvatarHandlerGetRedirectsToSignedDownload(t *testing.T) {
	repository := &avatarRepositoryStub{
		currentURL: "https://cdn.test/users/user-1/avatars/avatar.png",
	}
	handler := NewAvatarHandler(repository, &avatarStorageStub{})
	ctx, recorder := newAvatarContext(http.MethodGet, "/avatars/user-1", "", "")
	ctx.SetParamNames("userId")
	ctx.SetParamValues("user-1")

	if err := handler.Get(ctx); err != nil {
		t.Fatalf("Get() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Get() status = %d, want %d", recorder.Code, http.StatusTemporaryRedirect)
	}
	if location := recorder.Header().Get("Location"); location != "https://bucket.test/download?signature=example" {
		t.Fatalf("Get() Location = %q", location)
	}
}

func TestValidateAvatar(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		size     int64
		wantErr  bool
	}{
		{name: "valid PNG", mimeType: "image/png", size: 1024},
		{name: "unsupported GIF", mimeType: "image/gif", size: 1024, wantErr: true},
		{name: "too large", mimeType: "image/jpeg", size: maxAvatarBytes + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateAvatar(tt.mimeType, tt.size)
			if tt.wantErr && err == nil {
				t.Fatal("validateAvatar() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateAvatar() unexpected error = %v", err)
			}
		})
	}
}

func newAvatarContext(
	method, target, body, userID string,
) (echo.Context, *httptest.ResponseRecorder) {
	app := echo.New()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}
	return ctx, recorder
}
