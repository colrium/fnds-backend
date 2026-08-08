package files

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const defaultMaxUploadBytes int64 = 100 << 20

// CVDocumentTypeID is the stable document type ID for CV uploads.
const CVDocumentTypeID = "cv-document-type"

// cvMaxUploadBytes is the maximum file size allowed for CV uploads (10 MB).
const cvMaxUploadBytes int64 = 10 * 1024 * 1024

// ValidationError is a file-upload validation failure with a stable,
// machine-readable code so callers (the frontend's error-message mapping)
// can key off Code instead of parsing Message text.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// Unwrap lets errors.Is(err, ErrInvalid) keep working for existing callers
// that only care whether a request was invalid, not the specific reason.
func (e *ValidationError) Unwrap() error {
	return ErrInvalid
}

var (
	ErrInvalid  = errors.New("invalid file upload")
	ErrNotFound = errors.New("file not found")

	// ErrDuplicateCV is returned when a concurrent upload has already claimed
	// the single CV slot for a user, detected via the unique index on
	// {user_id, document_type_id} for CV records.
	ErrDuplicateCV = errors.New("cv already exists for user")

	safeFileNamePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

	// allowedCVMimeTypes restricts CV uploads to PDF and DOCX files only.
	allowedCVMimeTypes = map[string]struct{}{
		"application/pdf": {},
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {},
	}

	allowedMimeTypes = map[string]struct{}{
		"application/msword":       {},
		"application/pdf":          {},
		"application/vnd.ms-excel": {},
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       {},
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {},
		"audio/mpeg": {},
		"image/gif":  {},
		"image/jpeg": {},
		"image/png":  {},
		"image/webp": {},
		"text/csv":   {},
		"text/plain": {},
		"video/mp4":  {},
		"video/webm": {},
	}
)

type UploadRequest struct {
	DocumentTypeID string `json:"document_type_id"`
	FileName       string `json:"file_name"`
	RelativePath   string `json:"relative_path"`
	MimeType       string `json:"mime_type"`
	Size           int64  `json:"size"`
}

type CompleteUploadRequest struct {
	UploadRequest
	ObjectKey string `json:"object_key"`
}

func validateUpload(req UploadRequest, maxUploadBytes int64) error {
	if strings.TrimSpace(req.DocumentTypeID) == "" {
		return &ValidationError{Code: "invalid_request", Message: "document_type_id is required"}
	}
	if strings.TrimSpace(req.FileName) == "" || len(req.FileName) > 255 {
		return &ValidationError{Code: "invalid_request", Message: "file_name is required and must not exceed 255 characters"}
	}
	if len(req.RelativePath) > 1024 {
		return &ValidationError{Code: "invalid_request", Message: "relative_path must not exceed 1024 characters"}
	}
	if _, allowed := allowedMimeTypes[strings.ToLower(strings.TrimSpace(req.MimeType))]; !allowed {
		return &ValidationError{Code: "invalid_file_type", Message: "unsupported mime_type"}
	}
	if maxUploadBytes <= 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	if req.Size <= 0 {
		return &ValidationError{Code: "file_empty", Message: "file cannot be empty"}
	}
	if req.Size > maxUploadBytes {
		return &ValidationError{Code: "file_too_large", Message: fmt.Sprintf("size must be at most %d bytes", maxUploadBytes)}
	}
	return nil
}

// ValidateCVUpload validates an upload request specifically for CV documents.
// It enforces that only PDF and DOCX files are accepted and that the file size
// is greater than 0 and at most 10,485,760 bytes (10 MB).
func ValidateCVUpload(req UploadRequest) error {
	mimeType := strings.ToLower(strings.TrimSpace(req.MimeType))
	if _, allowed := allowedCVMimeTypes[mimeType]; !allowed {
		return &ValidationError{Code: "invalid_file_type", Message: "Only PDF and DOCX files are accepted for CV upload"}
	}
	if req.Size <= 0 {
		return &ValidationError{Code: "file_empty", Message: "File cannot be empty"}
	}
	if req.Size > cvMaxUploadBytes {
		return &ValidationError{Code: "file_too_large", Message: "CV file must be smaller than 10 MB"}
	}
	return nil
}

func buildObjectKey(userID, fileName string) string {
	name := filepath.Base(strings.ReplaceAll(fileName, `\`, "/"))
	name = safeFileNamePattern.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = "file"
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(ext) >= 120 {
			name = name[:120]
		} else {
			maxBase := 120 - len(ext)
			if len(base) > maxBase {
				base = base[:maxBase]
			}
			name = base + ext
		}
	}
	return fmt.Sprintf("users/%s/%s/%s", userID, uuid.New().String(), name)
}

func objectBelongsToUser(objectKey, userID string) bool {
	return strings.HasPrefix(objectKey, "users/"+userID+"/")
}
