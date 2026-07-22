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

var (
	ErrInvalid  = errors.New("invalid file upload")
	ErrNotFound = errors.New("file not found")

	safeFileNamePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	allowedMimeTypes    = map[string]struct{}{
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
		return fmt.Errorf("%w: document_type_id is required", ErrInvalid)
	}
	if strings.TrimSpace(req.FileName) == "" || len(req.FileName) > 255 {
		return fmt.Errorf("%w: file_name is required and must not exceed 255 characters", ErrInvalid)
	}
	if len(req.RelativePath) > 1024 {
		return fmt.Errorf("%w: relative_path must not exceed 1024 characters", ErrInvalid)
	}
	if _, allowed := allowedMimeTypes[strings.ToLower(strings.TrimSpace(req.MimeType))]; !allowed {
		return fmt.Errorf("%w: unsupported mime_type", ErrInvalid)
	}
	if maxUploadBytes <= 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	if req.Size <= 0 || req.Size > maxUploadBytes {
		return fmt.Errorf("%w: size must be between 1 and %d bytes", ErrInvalid, maxUploadBytes)
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
		maxBase := 120 - len(ext)
		if maxBase < 1 {
			maxBase = 120
			ext = ""
		}
		name = base[:maxBase] + ext
	}
	return fmt.Sprintf("users/%s/%s/%s", userID, uuid.New().String(), name)
}

func objectBelongsToUser(objectKey, userID string) bool {
	return strings.HasPrefix(objectKey, "users/"+userID+"/")
}
