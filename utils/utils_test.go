package utils

import (
	"testing"
)

func TestGetFileMimeType(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"photo.png", "image/png"},
		{"photo.gif", "image/gif"},
		{"doc.pdf", "application/pdf"},
		{"doc.docx", "application/msword"},
		{"data.csv", "text/csv"},
		{"readme.txt", "text/plain"},
		{"video.mp4", "video/mp4"},
		{"audio.mp3", "audio/mpeg"},
		{"unknown.xyz", "application/octet-stream"},
		{"noextension", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			if got := GetFileMimeType(tt.filename); got != tt.want {
				t.Errorf("GetFileMimeType(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestAllowedFileType(t *testing.T) {
	allowed := []string{".jpg", ".png", ".pdf"}

	tests := []struct {
		filename string
		want     bool
	}{
		{"photo.jpg", true},
		{"photo.png", true},
		{"doc.pdf", true},
		{"photo.gif", false},
		{"doc.docx", false},
		{"script.exe", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			if got := AllowedFileType(tt.filename, allowed); got != tt.want {
				t.Errorf("AllowedFileType(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}
