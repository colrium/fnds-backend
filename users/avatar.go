package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"backend/configs"
	"backend/contextutil"
	"backend/files"
	"backend/responses"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/time/rate"
)

const maxAvatarBytes int64 = 5 << 20

var (
	errAvatarNotFound = errors.New("avatar not found")
	avatarMimeTypes   = map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
	}
)

type AvatarRepository interface {
	GetAvatarURL(ctx context.Context, userID string) (string, error)
	SetAvatarURL(ctx context.Context, userID, avatarURL string) error
}

type MongoAvatarRepository struct{}

func (r *MongoAvatarRepository) GetAvatarURL(ctx context.Context, userID string) (string, error) {
	var user struct {
		AvatarID string `bson:"avatar_id"`
	}
	err := configs.MI.DB.Collection("users").
		FindOne(ctx, bson.M{"_id": userID}).
		Decode(&user)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", errAvatarNotFound
		}
		return "", fmt.Errorf("getting avatar URL: %w", err)
	}
	return user.AvatarID, nil
}

func (r *MongoAvatarRepository) SetAvatarURL(
	ctx context.Context,
	userID, avatarURL string,
) error {
	result, err := configs.MI.DB.Collection("users").UpdateOne(
		ctx,
		bson.M{"_id": userID},
		bson.M{"$set": bson.M{
			"avatar_id":  avatarURL,
			"updated_at": time.Now().UTC(),
		}},
	)
	if err != nil {
		return fmt.Errorf("updating avatar URL: %w", err)
	}
	if result.MatchedCount == 0 {
		return errAvatarNotFound
	}
	return nil
}

type AvatarHandler struct {
	repository AvatarRepository
	storage    files.Storage
}

type avatarPresignRequest struct {
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

type avatarCompleteRequest struct {
	ObjectKey string `json:"object_key"`
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
}

func NewAvatarHandler(repository AvatarRepository, storage files.Storage) *AvatarHandler {
	return &AvatarHandler{repository: repository, storage: storage}
}

func RegisterAvatarAuthRoutes(g *echo.Group, handler *AvatarHandler) {
	g.POST(
		"/profile/avatar/presign",
		handler.Presign,
		avatarRateLimiter(rate.Limit(2), 5),
	)
	g.POST("/profile/avatar/complete", handler.Complete)
}

func RegisterAvatarPublicRoutes(g *echo.Group, handler *AvatarHandler) {
	g.GET(
		"/avatars/:userId",
		handler.Get,
		avatarRateLimiter(rate.Limit(10), 30),
	)
}

func avatarRateLimiter(requestRate rate.Limit, burst int) echo.MiddlewareFunc {
	return echomiddleware.RateLimiterWithConfig(echomiddleware.RateLimiterConfig{
		Store: echomiddleware.NewRateLimiterMemoryStoreWithConfig(
			echomiddleware.RateLimiterMemoryStoreConfig{
				Rate:      requestRate,
				Burst:     burst,
				ExpiresIn: 10 * time.Minute,
			},
		),
	})
}

func (h *AvatarHandler) Presign(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	var req avatarPresignRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid request body")
	}
	req.MimeType = strings.ToLower(strings.TrimSpace(req.MimeType))
	extension, err := validateAvatar(req.MimeType, req.Size)
	if err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.FileName) == "" {
		return responses.SendError(ctx, http.StatusBadRequest, "file_name is required")
	}

	objectKey := fmt.Sprintf(
		"users/%s/avatars/%s%s",
		userID,
		uuid.New().String(),
		extension,
	)
	intent, err := h.storage.PresignUpload(
		ctx.Request().Context(),
		objectKey,
		req.MimeType,
		userID,
		req.Size,
	)
	if err != nil {
		return responses.SendError(ctx, http.StatusBadGateway, "Failed to prepare avatar upload")
	}
	return responses.SendOK(ctx, intent, "Avatar upload prepared successfully")
}

func (h *AvatarHandler) Complete(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, http.StatusUnauthorized, "Unauthorized")
	}

	var req avatarCompleteRequest
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid request body")
	}
	req.MimeType = strings.ToLower(strings.TrimSpace(req.MimeType))
	if _, err := validateAvatar(req.MimeType, req.Size); err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, err.Error())
	}
	if !avatarBelongsToUser(req.ObjectKey, userID) {
		return responses.SendError(ctx, http.StatusForbidden, "Avatar upload does not belong to this user")
	}

	requestCtx := ctx.Request().Context()
	object, err := h.storage.Stat(requestCtx, req.ObjectKey)
	if err != nil {
		return responses.SendError(ctx, http.StatusBadRequest, "Uploaded avatar could not be verified")
	}
	if object.Size != req.Size ||
		!strings.EqualFold(object.ContentType, req.MimeType) ||
		object.UserID != userID {
		return responses.SendError(ctx, http.StatusBadRequest, "Uploaded avatar metadata does not match")
	}
	if err := h.storage.MarkComplete(requestCtx, req.ObjectKey); err != nil {
		return responses.SendError(ctx, http.StatusBadGateway, "Avatar upload could not be finalized")
	}

	previousURL, err := h.repository.GetAvatarURL(requestCtx, userID)
	if err != nil && !errors.Is(err, errAvatarNotFound) {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to load current avatar")
	}
	storedURL := h.storage.PublicURL(req.ObjectKey)
	if err := h.repository.SetAvatarURL(requestCtx, userID, storedURL); err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to save avatar")
	}
	h.deletePreviousAvatar(requestCtx, previousURL, req.ObjectKey, userID)

	return responses.SendOK(
		ctx,
		map[string]string{
			"avatar": AvatarPublicURL(userID, filepath.Base(req.ObjectKey)),
		},
		"Avatar updated successfully",
	)
}

func (h *AvatarHandler) Get(ctx echo.Context) error {
	userID := ctx.Param("userId")
	if userID == "" {
		return responses.SendError(ctx, http.StatusBadRequest, "User ID is required")
	}
	storedURL, err := h.repository.GetAvatarURL(ctx.Request().Context(), userID)
	if err != nil || storedURL == "" {
		return responses.SendError(ctx, http.StatusNotFound, "Avatar not found")
	}
	objectKey, err := h.storage.KeyFromURL(storedURL)
	if err != nil || !avatarBelongsToUser(objectKey, userID) {
		return responses.SendError(ctx, http.StatusNotFound, "Avatar not found")
	}
	downloadURL, err := h.storage.PresignDownload(ctx.Request().Context(), objectKey)
	if err != nil {
		return responses.SendError(ctx, http.StatusBadGateway, "Failed to prepare avatar")
	}
	ctx.Response().Header().Set("Cache-Control", "public, max-age=300")
	return ctx.Redirect(http.StatusTemporaryRedirect, downloadURL)
}

func AvatarPublicURL(userID, version string) string {
	baseURL := strings.TrimRight(configs.APIPublicURL(), "/")
	avatarURL := baseURL + "/api/v1/avatars/" + url.PathEscape(userID)
	if version != "" {
		avatarURL += "?v=" + url.QueryEscape(version)
	}
	return avatarURL
}

func validateAvatar(mimeType string, size int64) (string, error) {
	extension, allowed := avatarMimeTypes[mimeType]
	if !allowed {
		return "", fmt.Errorf("avatar must be a JPEG, PNG, or WebP image")
	}
	if size <= 0 || size > maxAvatarBytes {
		return "", fmt.Errorf("avatar size must be between 1 byte and 5 MB")
	}
	return extension, nil
}

func avatarBelongsToUser(objectKey, userID string) bool {
	return strings.HasPrefix(objectKey, "users/"+userID+"/avatars/") &&
		filepath.Base(objectKey) != "."
}

func (h *AvatarHandler) deletePreviousAvatar(
	ctx context.Context,
	previousURL, currentKey, userID string,
) {
	if previousURL == "" {
		return
	}
	previousKey, err := h.storage.KeyFromURL(previousURL)
	if err != nil || previousKey == currentKey || !avatarBelongsToUser(previousKey, userID) {
		return
	}
	if err := h.storage.Delete(ctx, previousKey); err != nil {
		slog.Warn("failed to delete previous avatar", "user_id", userID, "error", err)
	}
}
