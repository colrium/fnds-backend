package files

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateUpload(t *testing.T) {
	t.Parallel()

	valid := UploadRequest{
		DocumentTypeID: "type-1",
		FileName:       "certificate.pdf",
		MimeType:       "application/pdf",
		Size:           1024,
	}
	tests := []struct {
		name    string
		mutate  func(*UploadRequest)
		wantErr bool
	}{
		{
			name:   "valid",
			mutate: func(*UploadRequest) {},
		},
		{
			name:    "missing document type",
			mutate:  func(req *UploadRequest) { req.DocumentTypeID = "" },
			wantErr: true,
		},
		{
			name:    "unsupported mime type",
			mutate:  func(req *UploadRequest) { req.MimeType = "application/x-msdownload" },
			wantErr: true,
		},
		{
			name:    "empty file",
			mutate:  func(req *UploadRequest) { req.Size = 0 },
			wantErr: true,
		},
		{
			name:    "file exceeds limit",
			mutate:  func(req *UploadRequest) { req.Size = 2049 },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := valid
			tt.mutate(&req)
			err := validateUpload(req, 2048)
			if tt.wantErr && !errors.Is(err, ErrInvalid) {
				t.Fatalf("validateUpload() error = %v, want ErrInvalid", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateUpload() unexpected error = %v", err)
			}
		})
	}
}

func TestBuildObjectKeyScopesAndSanitizes(t *testing.T) {
	t.Parallel()

	key := buildObjectKey("user-1", "../../unsafe name.pdf")
	if !strings.HasPrefix(key, "users/user-1/") {
		t.Fatalf("buildObjectKey() = %q, want user prefix", key)
	}
	if strings.Contains(key, "..") || strings.Contains(key, " ") {
		t.Fatalf("buildObjectKey() = %q, want sanitized key", key)
	}
	if !objectBelongsToUser(key, "user-1") {
		t.Fatalf("objectBelongsToUser(%q) = false, want true", key)
	}
	if objectBelongsToUser(key, "user-2") {
		t.Fatalf("objectBelongsToUser(%q) = true for another user", key)
	}
}

func TestBuildObjectKeyHandlesLongExtension(t *testing.T) {
	t.Parallel()

	key := buildObjectKey("user-1", "a."+strings.Repeat("x", 200))
	name := key[strings.LastIndex(key, "/")+1:]
	if len(name) > 120 {
		t.Fatalf("buildObjectKey() name length = %d, want at most 120", len(name))
	}
}

func TestValidateCVUpload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     UploadRequest
		wantErr bool
	}{
		{
			name: "valid PDF",
			req: UploadRequest{
				MimeType: "application/pdf",
				Size:     1024,
			},
		},
		{
			name: "valid DOCX",
			req: UploadRequest{
				MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				Size:     2048,
			},
		},
		{
			name: "rejected JPEG",
			req: UploadRequest{
				MimeType: "image/jpeg",
				Size:     1024,
			},
			wantErr: true,
		},
		{
			name: "rejected plain text",
			req: UploadRequest{
				MimeType: "text/plain",
				Size:     1024,
			},
			wantErr: true,
		},
		{
			name: "zero size",
			req: UploadRequest{
				MimeType: "application/pdf",
				Size:     0,
			},
			wantErr: true,
		},
		{
			name: "exceeds 10 MB",
			req: UploadRequest{
				MimeType: "application/pdf",
				Size:     11 * 1024 * 1024,
			},
			wantErr: true,
		},
		{
			name: "exactly 10 MB is allowed",
			req: UploadRequest{
				MimeType: "application/pdf",
				Size:     10 * 1024 * 1024,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCVUpload(tt.req)
			if tt.wantErr && err == nil {
				t.Fatal("ValidateCVUpload() expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateCVUpload() unexpected error = %v", err)
			}
			if tt.wantErr && err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateCVUpload() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestS3StorePublicURLRoundTrip(t *testing.T) {
	t.Parallel()

	store := &S3Store{publicBaseURL: "https://cdn.example.com/files"}
	key := "users/user-1/object/a file.pdf"
	fileURL := store.PublicURL(key)

	got, err := store.KeyFromURL(fileURL)
	if err != nil {
		t.Fatalf("KeyFromURL() unexpected error = %v", err)
	}
	if got != key {
		t.Fatalf("KeyFromURL() = %q, want %q", got, key)
	}
}
