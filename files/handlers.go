package files

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

type Handler struct {
	repository     Repository
	storage        Storage
	maxUploadBytes int64
}

type FileResponse struct {
	File
	DownloadURL string `json:"download_url"`
}

func NewHandler(repository Repository, storage Storage, maxUploadBytes int64) *Handler {
	if maxUploadBytes <= 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	return &Handler{
		repository:     repository,
		storage:        storage,
		maxUploadBytes: maxUploadBytes,
	}
}

// sendValidationError responds with the structured {error: {code, message}}
// envelope for a *ValidationError so callers (e.g. CVUploader's error-code
// mapping) can key off a stable code instead of parsing message text.
func sendValidationError(ctx echo.Context, err error) error {
	if validationErr, ok := errors.AsType[*ValidationError](err); ok {
		return responses.SendErrorWithCode(ctx, http.StatusBadRequest,
			validationErr.Code, validationErr.Message, "", "", nil)
	}
	return responses.SendError(ctx, http.StatusBadRequest, err.Error())
}

func (h *Handler) List(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	fileList, err := h.repository.ListByUserID(ctx.Request().Context(), userID)
	if err != nil {
		slog.ErrorContext(ctx.Request().Context(), "failed to list files for user",
			"user_id", userID, "error", err)
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load files")
	}
	response := make([]FileResponse, 0, len(fileList))
	for _, file := range fileList {
		if file.FileURL == "" {
			slog.WarnContext(ctx.Request().Context(), "skipping file record with empty file URL",
				"user_id", userID, "file_id", file.ID)
			continue
		}
		objectKey, err := h.storage.KeyFromURL(file.FileURL)
		if err != nil || !objectBelongsToUser(objectKey, userID) {
			// Either a corrupted/foreign file_url or a data-integrity mismatch
			// between the record's owner and its object key — both indicate a
			// bug elsewhere, not a client mistake, so this is worth an ERROR.
			slog.ErrorContext(ctx.Request().Context(), "stored file URL is invalid or does not belong to user",
				"user_id", userID, "file_id", file.ID, "error", err)
			return responses.SendError(ctx, http.StatusInternalServerError, "Stored file URL is invalid")
		}
		downloadURL, err := h.storage.PresignDownload(ctx.Request().Context(), objectKey)
		if err != nil {
			slog.ErrorContext(ctx.Request().Context(), "failed to presign file download",
				"user_id", userID, "file_id", file.ID, "object_key", objectKey, "error", err)
			return responses.SendError(ctx, http.StatusBadGateway, "Failed to prepare file download")
		}
		response = append(response, FileResponse{File: file, DownloadURL: downloadURL})
	}
	return responses.SendOK(ctx, response, "Files retrieved successfully")
}

func (h *Handler) Presign(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	var req UploadRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid request body")
	}
	req.MimeType = strings.ToLower(strings.TrimSpace(req.MimeType))
	if err := validateUpload(req, h.maxUploadBytes); err != nil {
		return sendValidationError(ctx, err)
	}
	if req.DocumentTypeID == CVDocumentTypeID {
		if err := ValidateCVUpload(req); err != nil {
			return sendValidationError(ctx, err)
		}
	}

	objectKey := buildObjectKey(userID, req.FileName)
	intent, err := h.storage.PresignUpload(
		ctx.Request().Context(),
		objectKey,
		req.MimeType,
		userID,
		req.Size,
	)
	if err != nil {
		slog.ErrorContext(ctx.Request().Context(), "failed to presign file upload",
			"user_id", userID, "object_key", objectKey, "document_type_id", req.DocumentTypeID, "error", err)
		return responses.SendError(ctx, http.StatusBadGateway, "Failed to prepare file upload")
	}
	return responses.SendOK(ctx, intent, "Upload prepared successfully")
}

func (h *Handler) Complete(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	var req CompleteUploadRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid request body")
	}
	req.MimeType = strings.ToLower(strings.TrimSpace(req.MimeType))
	if err := validateUpload(req.UploadRequest, h.maxUploadBytes); err != nil {
		return sendValidationError(ctx, err)
	}
	if req.DocumentTypeID == CVDocumentTypeID {
		if err := ValidateCVUpload(req.UploadRequest); err != nil {
			return sendValidationError(ctx, err)
		}
	}
	if !objectBelongsToUser(req.ObjectKey, userID) {
		return responses.SendError(ctx, http.StatusForbidden, "Upload does not belong to this user")
	}

	object, err := h.storage.Stat(ctx.Request().Context(), req.ObjectKey)
	if err != nil {
		slog.WarnContext(ctx.Request().Context(), "could not verify uploaded object in storage",
			"user_id", userID, "object_key", req.ObjectKey, "error", err)
		return responses.SendError(ctx, http.StatusBadRequest, "Uploaded file could not be verified")
	}
	if object.Size != req.Size ||
		!strings.EqualFold(object.ContentType, req.MimeType) ||
		object.UserID != userID {
		slog.WarnContext(ctx.Request().Context(), "uploaded file metadata does not match completion request",
			"user_id", userID, "object_key", req.ObjectKey,
			"object_size", object.Size, "request_size", req.Size,
			"object_content_type", object.ContentType, "request_mime_type", req.MimeType,
			"object_user_id", object.UserID)
		return responses.SendError(ctx, http.StatusBadRequest, "Uploaded file metadata does not match")
	}

	file := &File{
		UserID:         userID,
		DocumentTypeID: req.DocumentTypeID,
		FileName:       req.FileName,
		FileURL:        h.storage.PublicURL(req.ObjectKey),
		RelativePath:   req.RelativePath,
		MimeType:       req.MimeType,
		Size:           req.Size,
	}
	if err := h.storage.MarkComplete(ctx.Request().Context(), req.ObjectKey); err != nil {
		slog.ErrorContext(ctx.Request().Context(), "failed to mark uploaded object complete",
			"user_id", userID, "object_key", req.ObjectKey, "error", err)
		return responses.SendError(ctx, http.StatusBadGateway, "Upload could not be finalized")
	}

	// CV replacement logic: ensure at most one CV file record per artisan.
	// Strategy: look up the existing CV (for later cleanup only — this is a
	// best-effort hint, not a correctness check), then create the new record
	// first. The unique index on {user_id, document_type_id} is what actually
	// enforces "at most one CV" and closes the TOCTOU window between the
	// lookup and the create: if two uploads race, the loser's Create fails
	// with ErrDuplicateCV and the winner's old CV (if any) is left untouched.
	// Only after the new record is safely committed do we clean up the old
	// record and its S3 object — this way a failed Create never destroys a
	// CV the artisan already had.
	var existingCV *File
	if req.DocumentTypeID == CVDocumentTypeID {
		existing, findErr := h.repository.FindByUserAndDocType(ctx.Request().Context(), userID, CVDocumentTypeID)
		if findErr != nil && !errors.Is(findErr, ErrNotFound) {
			slog.ErrorContext(ctx.Request().Context(), "failed to check for existing CV before replacement",
				"user_id", userID, "error", findErr)
			return responses.SendError(ctx, http.StatusInternalServerError, "Failed to check existing CV")
		}
		existingCV = existing
	}

	if err := h.repository.Create(ctx.Request().Context(), file); err != nil {
		if errors.Is(err, ErrDuplicateCV) {
			// Lost a race against a concurrent CV upload for the same user.
			// The old CV (if any) is untouched; clean up only the orphaned
			// new S3 object before returning.
			cleanupCtx := context.WithoutCancel(ctx.Request().Context())
			if delErr := h.storage.Delete(cleanupCtx, req.ObjectKey); delErr != nil {
				slog.ErrorContext(ctx.Request().Context(), "failed to clean up orphaned S3 object after duplicate CV race",
					"user_id", userID, "object_key", req.ObjectKey, "error", delErr)
			}
			slog.WarnContext(ctx.Request().Context(), "CV insert rejected by unique index; concurrent upload for the same user",
				"user_id", userID)
			return responses.SendError(ctx, http.StatusConflict, "A CV upload is already in progress for this account. Please retry")
		}
		// The old CV, if any, remains intact since Create failed.
		slog.ErrorContext(ctx.Request().Context(), "failed to create file record",
			"user_id", userID, "document_type_id", req.DocumentTypeID, "object_key", req.ObjectKey, "error", err)
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to save file")
	}

	// Best-effort cleanup of the old CV now that the new record is safely
	// committed. Use a context detached from the request so a client
	// disconnect does not abandon the cleanup. Delete the DB record before
	// the S3 object: a failed S3 delete only leaves an orphaned object for a
	// cleanup job to reclaim, whereas a failed DB delete after a successful
	// S3 delete would leave a "ghost" record pointing at a file that no
	// longer exists — a worse failure mode since the artisan would see the
	// old CV in their file list but get a 404 trying to download it.
	if existingCV != nil {
		cleanupCtx := context.WithoutCancel(ctx.Request().Context())
		if delErr := h.repository.DeleteByIDForUser(cleanupCtx, existingCV.ID, userID); delErr != nil {
			slog.ErrorContext(ctx.Request().Context(), "failed to delete old CV record during replacement cleanup",
				"user_id", userID, "file_id", existingCV.ID, "error", delErr)
		} else if oldKey, keyErr := h.storage.KeyFromURL(existingCV.FileURL); keyErr != nil {
			slog.ErrorContext(ctx.Request().Context(), "could not derive object key from old CV's file URL for cleanup",
				"user_id", userID, "file_id", existingCV.ID, "error", keyErr)
		} else if delErr := h.storage.Delete(cleanupCtx, oldKey); delErr != nil {
			slog.ErrorContext(ctx.Request().Context(), "deleted old CV record but failed to delete its S3 object during replacement cleanup",
				"user_id", userID, "file_id", existingCV.ID, "object_key", oldKey, "error", delErr)
		}
	}
	downloadURL, err := h.storage.PresignDownload(ctx.Request().Context(), req.ObjectKey)
	if err != nil {
		slog.ErrorContext(ctx.Request().Context(), "file record saved but failed to presign its download",
			"user_id", userID, "file_id", file.ID, "object_key", req.ObjectKey, "error", err)
		return responses.SendError(ctx, http.StatusBadGateway, "File saved but download could not be prepared")
	}
	return responses.SendCreated(
		ctx,
		FileResponse{File: *file, DownloadURL: downloadURL},
		"File uploaded successfully",
	)
}

func (h *Handler) Delete(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	requestCtx := ctx.Request().Context()
	fileID := ctx.Param("id")
	file, err := h.repository.GetByIDForUser(requestCtx, fileID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(ctx, http.StatusNotFound, "File not found")
		}
		slog.ErrorContext(requestCtx, "failed to load file for deletion",
			"user_id", userID, "file_id", fileID, "error", err)
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load file")
	}

	objectKey, err := h.storage.KeyFromURL(file.FileURL)
	if err != nil || !objectBelongsToUser(objectKey, userID) {
		slog.ErrorContext(requestCtx, "stored file URL is invalid or does not belong to user",
			"user_id", userID, "file_id", fileID, "error", err)
		return responses.SendError(ctx, http.StatusInternalServerError, "Stored file URL is invalid")
	}
	if err := h.storage.Delete(requestCtx, objectKey); err != nil {
		slog.ErrorContext(requestCtx, "failed to delete stored file from S3",
			"user_id", userID, "file_id", fileID, "object_key", objectKey, "error", err)
		return responses.SendError(ctx, http.StatusBadGateway, "Failed to delete stored file")
	}
	if err := h.repository.DeleteByIDForUser(requestCtx, file.ID, userID); err != nil {
		slog.ErrorContext(requestCtx, "file deleted from S3 but failed to delete its file record",
			"user_id", userID, "file_id", fileID, "error", err)
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to delete file record")
	}

	return responses.SendOK(ctx, nil, "File deleted successfully")
}
