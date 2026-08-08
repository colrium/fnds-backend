package cvparser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend/contextutil"
	"backend/files"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

// defaultParseTimeout is the maximum duration allowed for the entire parse
// operation (download + extraction + mapping).
const defaultParseTimeout = 30 * time.Second

// statTimeout bounds the size-check HeadObject call to a small slice of the
// overall parse budget, so an S3 hiccup on that fast-path optimization can't
// eat into the time available for the actual download and extraction.
const statTimeout = 3 * time.Second

// ParseCVRequest is the expected JSON body for the POST /api/v1/cv/parse endpoint.
type ParseCVRequest struct {
	FileID string `json:"file_id" validate:"required"`
}

// ParseCVResponse is the response returned on successful CV parsing.
type ParseCVResponse struct {
	AutofillPayload *AutofillPayload `json:"autofill_payload"`
	FieldCount      int              `json:"field_count"`
	Message         string           `json:"message"`
}

// Handler handles CV parse requests. It depends on the files repository and
// storage for file retrieval/ownership verification, a Parser for extraction,
// and a Mapper for converting extraction results to autofill payloads.
type Handler struct {
	fileRepo files.Repository
	storage  files.Storage
	parser   Parser
	mapper   *Mapper
}

// NewHandler creates a new CV parse handler with the given dependencies.
func NewHandler(fileRepo files.Repository, storage files.Storage, parser Parser, mapper *Mapper) *Handler {
	return &Handler{
		fileRepo: fileRepo,
		storage:  storage,
		parser:   parser,
		mapper:   mapper,
	}
}

// ParseCV handles POST /api/v1/cv/parse requests. It validates the request,
// verifies file ownership, downloads the PDF, parses it, maps the results, and
// returns the autofill payload.
func (h *Handler) ParseCV(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}

	// Bind and validate request.
	var req ParseCVRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "Invalid request body")
	}
	if strings.TrimSpace(req.FileID) == "" {
		return responses.SendErrorWithCode(c, http.StatusBadRequest,
			"invalid_request", "file_id is required", "file_id", "", nil)
	}

	// Look up the file record; ensure it belongs to the authenticated user.
	file, err := h.fileRepo.GetByIDForUser(c.Request().Context(), req.FileID, userID)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			return responses.SendErrorWithCode(c, http.StatusNotFound,
				"file_not_found", "The referenced file could not be found", "", "", nil)
		}
		slog.ErrorContext(c.Request().Context(), "failed to look up CV file record",
			"user_id", userID, "file_id", req.FileID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "Failed to retrieve file")
	}

	// Derive the S3 object key from the stored file URL. Either failure here
	// points at a corrupted or unexpectedly-shaped file record — something an
	// operator should investigate rather than a routine client mistake.
	if strings.TrimSpace(file.FileURL) == "" {
		slog.ErrorContext(c.Request().Context(), "CV file record has an empty file URL",
			"user_id", userID, "file_id", req.FileID)
		return responses.SendError(c, http.StatusInternalServerError, "Stored file URL is invalid")
	}
	objectKey, err := h.storage.KeyFromURL(file.FileURL)
	if err != nil {
		slog.ErrorContext(c.Request().Context(), "failed to derive S3 object key from stored file URL",
			"user_id", userID, "file_id", req.FileID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "Stored file URL is invalid")
	}

	// Verify the object key belongs to the authenticated user. A mismatch here
	// means either data corruption or an authenticated user probing another
	// user's file, so it is worth a WARN even though the response itself
	// discloses nothing about the real owner.
	if !objectKeyBelongsToUser(objectKey, userID) {
		slog.WarnContext(c.Request().Context(), "CV parse request denied: object key does not belong to caller",
			"user_id", userID, "file_id", req.FileID)
		return responses.SendErrorWithCode(c, http.StatusForbidden,
			"ownership_mismatch", "You do not have permission to access this file", "", "", nil)
	}

	// Create a context with the parse timeout for the stat + download + extraction flow.
	parseCtx, cancel := context.WithTimeout(c.Request().Context(), defaultParseTimeout)
	defer cancel()

	// Check the object's size before downloading it, so an oversized file is
	// rejected without paying for a full download that the parser would
	// reject anyway. This is a fast-path optimization, not the real
	// constraint (the parser's size limit is enforced regardless), so it
	// gets its own short timeout rather than the full parse budget.
	statCtx, statCancel := context.WithTimeout(parseCtx, statTimeout)
	object, err := h.storage.Stat(statCtx, objectKey)
	statCancel()
	if err != nil {
		slog.ErrorContext(c.Request().Context(), "failed to stat CV object in storage before parsing",
			"user_id", userID, "file_id", req.FileID, "object_key", objectKey, "error", err)
		return responses.SendErrorWithCode(c, http.StatusBadGateway,
			"parser_unavailable", "CV parsing service is temporarily unavailable", "", "", nil)
	}
	if object.Size > maxDocumentBytes {
		slog.InfoContext(c.Request().Context(), "CV object exceeds parser size limit, skipping download",
			"user_id", userID, "file_id", req.FileID, "object_size", object.Size, "max_bytes", maxDocumentBytes)
		return h.mapParseError(c, fmt.Errorf("%w: object size %d bytes exceeds provider limit", ErrPDFTooLarge, object.Size), userID, req.FileID)
	}

	// Download the document bytes from S3.
	docBytes, err := h.storage.Download(parseCtx, objectKey)
	if err != nil {
		if parseCtx.Err() != nil {
			slog.WarnContext(c.Request().Context(), "CV download from storage exceeded the parse timeout",
				"user_id", userID, "file_id", req.FileID, "timeout", defaultParseTimeout, "error", err)
			return responses.SendErrorWithCode(c, http.StatusGatewayTimeout,
				"parse_timeout", "CV processing timed out. Please try again", "", "", nil)
		}
		slog.ErrorContext(c.Request().Context(), "failed to download CV from storage for parsing",
			"user_id", userID, "file_id", req.FileID, "object_key", objectKey, "error", err)
		return responses.SendErrorWithCode(c, http.StatusBadGateway,
			"parser_unavailable", "CV parsing service is temporarily unavailable", "", "", nil)
	}

	// Parse using the file's stored MIME type so the document is labeled
	// with its real format (PDF or DOCX) instead of being assumed to be PDF.
	result, err := h.parser.Extract(parseCtx, docBytes, file.MimeType)
	if err != nil {
		return h.mapParseError(c, err, userID, req.FileID)
	}

	// Map the extraction result to an autofill payload.
	payload := h.mapper.MapToAutofill(result)

	// Count populated fields in the payload.
	fieldCount := countPopulatedFields(payload)

	message := "CV parsed successfully"
	if fieldCount == 0 {
		// The model returned a well-formed but empty result — not a parser
		// failure, but worth a WARN: a run of these for otherwise-valid
		// uploads would point at a systematic extraction problem (e.g. a CV
		// layout the model handles poorly) that a single unreadable-PDF error
		// would not surface.
		message = "CV parsed but no fields could be extracted"
		slog.WarnContext(c.Request().Context(), "CV parsed successfully but no fields were extracted",
			"user_id", userID, "file_id", req.FileID)
	} else {
		slog.InfoContext(c.Request().Context(), "CV parsed successfully",
			"user_id", userID, "file_id", req.FileID, "field_count", fieldCount)
	}

	return responses.SendOK(c, ParseCVResponse{
		AutofillPayload: payload,
		FieldCount:      fieldCount,
		Message:         message,
	}, message)
}

// mapParseError translates domain errors from the parser into appropriate HTTP
// responses with structured error codes.
//
// ErrPDFUnreadable and ErrPDFPasswordProtected are routine, user-caused
// outcomes and are not logged; the response itself already tells the
// artisan what to do. Every other case reflects the OpenRouter integration or
// the parser itself misbehaving, which an operator needs visibility into to
// notice provider outages, quota exhaustion, or a broken parser regression.
func (h *Handler) mapParseError(c echo.Context, err error, userID, fileID string) error {
	switch {
	case errors.Is(err, ErrPDFUnreadable):
		return responses.SendErrorWithCode(c, http.StatusUnprocessableEntity,
			"pdf_unreadable", "The PDF content could not be read. Please upload a text-based PDF", "", "", nil)
	case errors.Is(err, ErrPDFPasswordProtected):
		return responses.SendErrorWithCode(c, http.StatusUnprocessableEntity,
			"pdf_password_protected", "The PDF is password-protected. Please upload an unprotected version", "", "", nil)
	case errors.Is(err, ErrPDFTooLarge):
		return responses.SendErrorWithCode(c, http.StatusUnprocessableEntity,
			"pdf_too_large", "The PDF file is too large. Please upload a smaller file (max 4.5 MB)", "", "", nil)
	case errors.Is(err, ErrParseTimeout):
		slog.WarnContext(c.Request().Context(), "CV extraction exceeded the parse timeout",
			"user_id", userID, "file_id", fileID, "timeout", defaultParseTimeout, "error", err)
		return responses.SendErrorWithCode(c, http.StatusGatewayTimeout,
			"parse_timeout", "CV processing timed out. Please try again", "", "", nil)
	case errors.Is(err, ErrParserUnavailable):
		slog.ErrorContext(c.Request().Context(), "CV extraction provider unavailable",
			"user_id", userID, "file_id", fileID, "error", err)
		return responses.SendErrorWithCode(c, http.StatusBadGateway,
			"parser_unavailable", "CV parsing service is temporarily unavailable", "", "", nil)
	case errors.Is(err, ErrUnsupportedDocumentFormat):
		// A file made it past upload validation with a MIME type this parser
		// has no Bedrock format mapping for — an upload/parser contract bug,
		// not something the artisan did wrong.
		slog.ErrorContext(c.Request().Context(), "CV file has a MIME type unsupported by the parser",
			"user_id", userID, "file_id", fileID, "error", err)
		return responses.SendErrorWithCode(c, http.StatusBadGateway,
			"parser_unavailable", "CV parsing service is temporarily unavailable", "", "", nil)
	default:
		slog.ErrorContext(c.Request().Context(), "unexpected error during CV extraction",
			"user_id", userID, "file_id", fileID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "An unexpected error occurred during CV parsing")
	}
}

// objectKeyBelongsToUser checks if the S3 object key follows the user-scoped
// prefix pattern: users/{userID}/...
func objectKeyBelongsToUser(objectKey, userID string) bool {
	return strings.HasPrefix(objectKey, "users/"+userID+"/")
}

// countPopulatedFields counts the number of non-nil fields in the autofill
// payload, including individual items in collection fields.
func countPopulatedFields(payload *AutofillPayload) int {
	if payload == nil {
		return 0
	}

	count := 0

	// Count scalar fields.
	if payload.Address != nil {
		count++
	}
	if payload.City != nil {
		count++
	}
	if payload.State != nil {
		count++
	}
	if payload.HighestEducationLevel != nil {
		count++
	}
	if payload.Certificate != nil {
		count++
	}
	if payload.Institution != nil {
		count++
	}
	if payload.OtherQualifications != nil {
		count++
	}
	if payload.ProfessionalBody != nil {
		count++
	}
	if payload.SkillAcquisitionMode != nil {
		count++
	}
	if payload.YearsOfExperience != nil {
		count++
	}

	// Count collection items.
	count += len(payload.PastEmployers)
	count += len(payload.LanguageProficiencies)
	count += len(payload.References)

	return count
}
