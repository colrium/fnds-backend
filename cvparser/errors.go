package cvparser

import "errors"

var (
	// ErrFileNotFound is returned when the referenced CV file does not exist.
	ErrFileNotFound = errors.New("file not found")

	// ErrOwnershipMismatch is returned when the authenticated user does not own the file.
	ErrOwnershipMismatch = errors.New("ownership mismatch")

	// ErrPDFUnreadable is returned when the PDF content cannot be extracted
	// (e.g., scanned image-only PDFs without an embedded text layer).
	ErrPDFUnreadable = errors.New("pdf unreadable")

	// ErrPDFPasswordProtected is returned when the PDF is password-protected
	// and cannot be processed.
	ErrPDFPasswordProtected = errors.New("pdf password protected")

	// ErrParseTimeout is returned when CV parsing exceeds the 30-second timeout.
	ErrParseTimeout = errors.New("parse timeout")

	// ErrParserUnavailable is returned when the Bedrock API is unavailable
	// or returns an unexpected error.
	ErrParserUnavailable = errors.New("parser unavailable")

	// ErrPDFTooLarge is returned when the PDF exceeds the provider's request
	// size limit.
	ErrPDFTooLarge = errors.New("pdf too large")
)
