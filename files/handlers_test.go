package files

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
)

type repositoryStub struct {
	created    *File
	files      []File
	file       *File
	err        error
	createErr  error
	existingCV *File
	findCVErr  error
	deleteErr  error
	deleted    []string
}

func (r *repositoryStub) Create(_ context.Context, file *File) error {
	r.created = file
	if r.createErr != nil {
		return r.createErr
	}
	return r.err
}

func (r *repositoryStub) ListByUserID(context.Context, string) ([]File, error) {
	return r.files, r.err
}

func (r *repositoryStub) GetByIDForUser(context.Context, string, string) (*File, error) {
	return r.file, r.err
}

func (r *repositoryStub) DeleteByIDForUser(_ context.Context, fileID string, _ string) error {
	r.deleted = append(r.deleted, fileID)
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return r.err
}

func (r *repositoryStub) FindByUserAndDocType(context.Context, string, string) (*File, error) {
	if r.existingCV != nil {
		return r.existingCV, nil
	}
	if r.findCVErr != nil {
		return nil, r.findCVErr
	}
	return nil, ErrNotFound
}

type storageStub struct {
	intent     *UploadIntent
	object     *ObjectInfo
	deletedKey string
	err        error
}

func (s *storageStub) PresignUpload(context.Context, string, string, string, int64) (*UploadIntent, error) {
	return s.intent, s.err
}

func (s *storageStub) PresignDownload(_ context.Context, objectKey string) (string, error) {
	return "https://signed.example.com/" + objectKey, s.err
}

func (s *storageStub) Download(_ context.Context, _ string) ([]byte, error) {
	return nil, s.err
}

func (s *storageStub) Stat(context.Context, string) (*ObjectInfo, error) {
	return s.object, s.err
}

func (s *storageStub) MarkComplete(context.Context, string) error {
	return s.err
}

func (s *storageStub) Delete(_ context.Context, objectKey string) error {
	s.deletedKey = objectKey
	return s.err
}

func (s *storageStub) PublicURL(objectKey string) string {
	return "https://cdn.example.com/" + objectKey
}

func (s *storageStub) KeyFromURL(fileURL string) (string, error) {
	const prefix = "https://cdn.example.com/"
	if !strings.HasPrefix(fileURL, prefix) {
		return "", errors.New("invalid URL")
	}
	return strings.TrimPrefix(fileURL, prefix), nil
}

func TestHandlerCompleteCreatesVerifiedFileRecord(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	storage := &storageStub{object: &ObjectInfo{
		Size:        1024,
		ContentType: "application/pdf",
		UserID:      "user-1",
	}}
	handler := NewHandler(repository, storage, 2048)
	body := `{
		"document_type_id":"type-1",
		"file_name":"certificate.pdf",
		"relative_path":"certificates/certificate.pdf",
		"mime_type":"application/pdf",
		"size":1024,
		"object_key":"users/user-1/object/certificate.pdf"
	}`
	ctx, recorder := newFileContext(http.MethodPost, "/files/complete", body, "user-1")

	if err := handler.Complete(ctx); err != nil {
		t.Fatalf("Complete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("Complete() status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if repository.created == nil {
		t.Fatal("Complete() did not create a file record")
	}
	if repository.created.FileURL != "https://cdn.example.com/users/user-1/object/certificate.pdf" {
		t.Fatalf("Complete() file URL = %q", repository.created.FileURL)
	}
}

func TestHandlerCompleteRejectsForeignObjectKey(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{}
	handler := NewHandler(repository, &storageStub{}, 2048)
	body := `{
		"document_type_id":"type-1",
		"file_name":"certificate.pdf",
		"mime_type":"application/pdf",
		"size":1024,
		"object_key":"users/user-2/object/certificate.pdf"
	}`
	ctx, recorder := newFileContext(http.MethodPost, "/files/complete", body, "user-1")

	if err := handler.Complete(ctx); err != nil {
		t.Fatalf("Complete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("Complete() status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if repository.created != nil {
		t.Fatal("Complete() created a record for a foreign object key")
	}
}

func TestHandlerDeleteRemovesOwnedObjectAndRecord(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{file: &File{
		ID:      "file-1",
		UserID:  "user-1",
		FileURL: "https://cdn.example.com/users/user-1/object/certificate.pdf",
	}}
	storage := &storageStub{}
	handler := NewHandler(repository, storage, 2048)
	ctx, recorder := newFileContext(http.MethodDelete, "/files/file-1", "", "user-1")
	ctx.SetParamNames("id")
	ctx.SetParamValues("file-1")

	if err := handler.Delete(ctx); err != nil {
		t.Fatalf("Delete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("Delete() status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if storage.deletedKey != "users/user-1/object/certificate.pdf" {
		t.Fatalf("Delete() key = %q", storage.deletedKey)
	}
}

func TestHandlerCompleteCVReplacementDeletesOldRecordAndS3(t *testing.T) {
	t.Parallel()

	existingCV := &File{
		ID:             "old-cv-id",
		UserID:         "user-1",
		DocumentTypeID: CVDocumentTypeID,
		FileURL:        "https://cdn.example.com/users/user-1/object/old-cv.pdf",
	}
	repository := &repositoryStub{existingCV: existingCV}
	storage := &storageStub{object: &ObjectInfo{
		Size:        2048,
		ContentType: "application/pdf",
		UserID:      "user-1",
	}}
	handler := NewHandler(repository, storage, cvMaxUploadBytes)
	body := `{
		"document_type_id":"` + CVDocumentTypeID + `",
		"file_name":"new-cv.pdf",
		"relative_path":"cv/new-cv.pdf",
		"mime_type":"application/pdf",
		"size":2048,
		"object_key":"users/user-1/object/new-cv.pdf"
	}`
	ctx, recorder := newFileContext(http.MethodPost, "/files/complete", body, "user-1")

	if err := handler.Complete(ctx); err != nil {
		t.Fatalf("Complete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("Complete() status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if repository.created == nil {
		t.Fatal("Complete() did not create a new CV record")
	}
	if len(repository.deleted) != 1 || repository.deleted[0] != "old-cv-id" {
		t.Fatalf("Complete() deleted = %v, want [old-cv-id]", repository.deleted)
	}
	if storage.deletedKey != "users/user-1/object/old-cv.pdf" {
		t.Fatalf("Complete() old S3 key deleted = %q, want old-cv.pdf key", storage.deletedKey)
	}
}

func TestHandlerCompleteCVDuplicateRaceCleansUpNewS3Object(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{createErr: ErrDuplicateCV}
	storage := &storageStub{object: &ObjectInfo{
		Size:        2048,
		ContentType: "application/pdf",
		UserID:      "user-1",
	}}
	handler := NewHandler(repository, storage, cvMaxUploadBytes)
	body := `{
		"document_type_id":"` + CVDocumentTypeID + `",
		"file_name":"new-cv.pdf",
		"relative_path":"cv/new-cv.pdf",
		"mime_type":"application/pdf",
		"size":2048,
		"object_key":"users/user-1/object/new-cv.pdf"
	}`
	ctx, recorder := newFileContext(http.MethodPost, "/files/complete", body, "user-1")

	if err := handler.Complete(ctx); err != nil {
		t.Fatalf("Complete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("Complete() status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if storage.deletedKey != "users/user-1/object/new-cv.pdf" {
		t.Fatalf("Complete() should clean up new S3 object, deletedKey = %q", storage.deletedKey)
	}
}

func TestHandlerCompleteCVNoExistingCVCreatesDirectly(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{} // FindByUserAndDocType returns ErrNotFound
	storage := &storageStub{object: &ObjectInfo{
		Size:        2048,
		ContentType: "application/pdf",
		UserID:      "user-1",
	}}
	handler := NewHandler(repository, storage, cvMaxUploadBytes)
	body := `{
		"document_type_id":"` + CVDocumentTypeID + `",
		"file_name":"first-cv.pdf",
		"relative_path":"cv/first-cv.pdf",
		"mime_type":"application/pdf",
		"size":2048,
		"object_key":"users/user-1/object/first-cv.pdf"
	}`
	ctx, recorder := newFileContext(http.MethodPost, "/files/complete", body, "user-1")

	if err := handler.Complete(ctx); err != nil {
		t.Fatalf("Complete() unexpected error = %v", err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("Complete() status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if repository.created == nil {
		t.Fatal("Complete() did not create a file record")
	}
	if len(repository.deleted) != 0 {
		t.Fatalf("Complete() should not delete anything, deleted = %v", repository.deleted)
	}
	if storage.deletedKey != "" {
		t.Fatalf("Complete() should not delete any S3 object, deletedKey = %q", storage.deletedKey)
	}
}

func newFileContext(method, target, body, userID string) (echo.Context, *httptest.ResponseRecorder) {
	app := echo.New()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	contextutil.SetUser(ctx, userID)
	return ctx, recorder
}
