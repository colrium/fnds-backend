package utils

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func SaveUploadedFile(file *multipart.FileHeader, uploadDir string) (string, error) {
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create upload directory: %w", err)
	}

	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	ext := filepath.Ext(file.Filename)
	name := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	filePath := filepath.Join(uploadDir, name)

	dst, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("failed to copy file content: %w", err)
	}

	return filePath, nil
}

func DeleteFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

func GetFileMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".doc", ".docx":
		return "application/msword"
	case ".xls", ".xlsx":
		return "application/vnd.ms-excel"
	case ".csv":
		return "text/csv"
	case ".txt":
		return "text/plain"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

func AllowedFileType(filename string, allowedExts []string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, allowed := range allowedExts {
		if ext == allowed {
			return true
		}
	}
	return false
}
