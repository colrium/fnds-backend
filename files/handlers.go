package files

import (
	"errors"
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

func (h *Handler) List(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	fileList, err := h.repository.ListByUserID(ctx.Request().Context(), userID)
	if err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load files")
	}
	response := make([]FileResponse, 0, len(fileList))
	for _, file := range fileList {
		objectKey, err := h.storage.KeyFromURL(file.FileURL)
		if err != nil || !objectBelongsToUser(objectKey, userID) {
			return responses.SendError(ctx, http.StatusInternalServerError, "Stored file URL is invalid")
		}
		downloadURL, err := h.storage.PresignDownload(ctx.Request().Context(), objectKey)
		if err != nil {
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
		return responses.SendError(ctx, http.StatusBadRequest, err.Error())
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
		return responses.SendError(ctx, http.StatusBadRequest, err.Error())
	}
	if !objectBelongsToUser(req.ObjectKey, userID) {
		return responses.SendError(ctx, http.StatusForbidden, "Upload does not belong to this user")
	}

	object, err := h.storage.Stat(ctx.Request().Context(), req.ObjectKey)
	if err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Uploaded file could not be verified")
	}
	if object.Size != req.Size ||
		!strings.EqualFold(object.ContentType, req.MimeType) ||
		object.UserID != userID {
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
	if err := h.repository.Create(ctx.Request().Context(), file); err != nil {
		if cleanupErr := h.storage.Delete(ctx.Request().Context(), req.ObjectKey); cleanupErr != nil {
			return responses.SendError(ctx, http.StatusBadGateway, "Failed to save or clean up uploaded file")
		}
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to save file")
	}
	downloadURL, err := h.storage.PresignDownload(ctx.Request().Context(), req.ObjectKey)
	if err != nil {
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
	file, err := h.repository.GetByIDForUser(requestCtx, ctx.Param("id"), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(ctx, http.StatusNotFound, "File not found")
		}
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load file")
	}

	objectKey, err := h.storage.KeyFromURL(file.FileURL)
	if err != nil || !objectBelongsToUser(objectKey, userID) {
		return responses.SendError(ctx, http.StatusInternalServerError, "Stored file URL is invalid")
	}
	if err := h.storage.Delete(requestCtx, objectKey); err != nil {
		return responses.SendError(ctx, http.StatusBadGateway, "Failed to delete stored file")
	}
	if err := h.repository.DeleteByIDForUser(requestCtx, file.ID, userID); err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to delete file record")
	}

	return responses.SendOK(ctx, nil, "File deleted successfully")
}
