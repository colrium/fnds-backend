package quotations

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/configs"
	"backend/contextutil"
	"backend/files"
	"backend/responses"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type QuotationAttachment struct {
	ID          string    `json:"id" bson:"_id"`
	QuotationID string    `json:"quotation_id" bson:"quotation_id"`
	OwnerID     string    `json:"owner_id" bson:"owner_id"`
	WorkspaceID string    `json:"workspace_id,omitempty" bson:"workspace_id,omitempty"`
	FileName    string    `json:"file_name" bson:"file_name"`
	ObjectKey   string    `json:"-" bson:"object_key"`
	MimeType    string    `json:"mime_type" bson:"mime_type"`
	Size        int64     `json:"size" bson:"size"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
}

type attachmentHandler struct {
	storage files.Storage
	maxSize int64
}

type attachmentUploadRequest struct {
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

type attachmentCompleteRequest struct {
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	ObjectKey string `json:"object_key"`
}

func RegisterAttachmentRoutes(g *echo.Group, storage files.Storage, maxSize int64) {
	h := &attachmentHandler{storage: storage, maxSize: maxSize}
	g.POST("/quotations/:id/attachments/presign", h.presign)
	g.POST("/quotations/:id/attachments/complete", h.complete)
	g.GET("/quotations/:id/attachments", h.list)
	g.GET("/quotations/:id/attachments/:attachmentID/download", h.download)
	g.DELETE("/quotations/:id/attachments/:attachmentID", h.delete)
}

func (h *attachmentHandler) presign(ctx echo.Context) error {
	userID, quotationID, err := h.authorizeOwner(ctx)
	if err != nil {
		return err
	}
	var req attachmentUploadRequest
	if err := ctx.Bind(&req); err != nil || !validAttachment(req.FileName, req.MimeType, req.Size, h.maxSize) {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid attachment")
	}
	objectKey := fmt.Sprintf("users/%s/quotations/%s/%s-%s", userID, quotationID, uuid.NewString(), safeName(req.FileName))
	intent, err := h.storage.PresignUpload(ctx.Request().Context(), objectKey, req.MimeType, userID, req.Size)
	if err != nil {
		return responses.SendError(ctx, http.StatusBadGateway, "Failed to prepare attachment upload")
	}
	return responses.SendOK(ctx, intent, "Attachment upload prepared")
}

func (h *attachmentHandler) complete(ctx echo.Context) error {
	userID, quotationID, err := h.authorizeOwner(ctx)
	if err != nil {
		return err
	}
	var req attachmentCompleteRequest
	if err := ctx.Bind(&req); err != nil || !validAttachment(req.FileName, req.MimeType, req.Size, h.maxSize) {
		return responses.SendError(ctx, http.StatusBadRequest, "Invalid attachment")
	}
	if !strings.HasPrefix(req.ObjectKey, "users/"+userID+"/quotations/"+quotationID+"/") {
		return responses.SendError(ctx, http.StatusForbidden, "Attachment does not belong to this quotation")
	}
	object, err := h.storage.Stat(ctx.Request().Context(), req.ObjectKey)
	if err != nil || object.Size != req.Size || !strings.EqualFold(object.ContentType, req.MimeType) || object.UserID != userID {
		return responses.SendError(ctx, http.StatusBadRequest, "Uploaded attachment could not be verified")
	}
	if err := h.storage.MarkComplete(ctx.Request().Context(), req.ObjectKey); err != nil {
		return responses.SendError(ctx, http.StatusBadGateway, "Attachment could not be finalized")
	}
	attachment := QuotationAttachment{
		ID:          uuid.NewString(),
		QuotationID: quotationID,
		OwnerID:     userID,
		FileName:    req.FileName,
		ObjectKey:   req.ObjectKey,
		MimeType:    req.MimeType,
		Size:        req.Size,
		CreatedAt:   time.Now().UTC(),
	}
	if _, err := configsCollection().InsertOne(ctx.Request().Context(), attachment); err != nil {
		return responses.SendError(ctx, http.StatusInternalServerError, "Failed to save attachment")
	}
	return responses.SendCreated(ctx, attachment, "Attachment uploaded")
}

func (h *attachmentHandler) list(ctx echo.Context) error {
	if err := h.authorizeParticipant(ctx); err != nil {
		return err
	}
	var attachments []QuotationAttachment
	cursor, err := configsCollection().Find(ctx.Request().Context(), bson.M{"quotation_id": ctx.Param("id")})
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to load attachments")
	}
	defer func() { _ = cursor.Close(ctx.Request().Context()) }()
	if err := cursor.All(ctx.Request().Context(), &attachments); err != nil {
		return responses.SendError(ctx, 500, "Failed to load attachments")
	}
	return responses.SendOK(ctx, attachments, "Attachments retrieved")
}

func (h *attachmentHandler) download(ctx echo.Context) error {
	if err := h.authorizeParticipant(ctx); err != nil {
		return err
	}
	var attachment QuotationAttachment
	err := configsCollection().FindOne(ctx.Request().Context(), bson.M{
		"_id": ctx.Param("attachmentID"), "quotation_id": ctx.Param("id"),
	}).Decode(&attachment)
	if err != nil {
		return responses.SendError(ctx, 404, "Attachment not found")
	}
	url, err := h.storage.PresignDownload(ctx.Request().Context(), attachment.ObjectKey)
	if err != nil {
		return responses.SendError(ctx, 502, "Failed to prepare attachment download")
	}
	return responses.SendOK(ctx, map[string]string{"download_url": url}, "Download prepared")
}

func (h *attachmentHandler) delete(ctx echo.Context) error {
	userID, _, err := h.authorizeOwner(ctx)
	if err != nil {
		return err
	}
	var attachment QuotationAttachment
	err = configsCollection().FindOne(ctx.Request().Context(), bson.M{
		"_id": ctx.Param("attachmentID"), "quotation_id": ctx.Param("id"), "owner_id": userID,
	}).Decode(&attachment)
	if err != nil {
		return responses.SendError(ctx, 404, "Attachment not found")
	}
	if err := h.storage.Delete(ctx.Request().Context(), attachment.ObjectKey); err != nil {
		return responses.SendError(ctx, 502, "Failed to delete attachment")
	}
	if _, err := configsCollection().DeleteOne(ctx.Request().Context(), bson.M{"_id": attachment.ID}); err != nil {
		return responses.SendError(ctx, 500, "Failed to delete attachment record")
	}
	return responses.SendOK(ctx, nil, "Attachment deleted")
}

func (h *attachmentHandler) authorizeOwner(ctx echo.Context) (string, string, error) {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return "", "", responses.SendError(ctx, 401, "Unauthorized")
	}
	q, err := GetQuotationByID(ctx.Param("id"))
	if err == nil {
		if q.ArtisanID != userID {
			return "", "", responses.SendError(ctx, 404, "Quotation not found")
		}
		return userID, q.ID, nil
	}
	eq, err := GetExternalQuotationByID(ctx.Param("id"))
	if err != nil || eq.ArtisanID != userID {
		return "", "", responses.SendError(ctx, 404, "Quotation not found")
	}
	return userID, eq.ID, nil
}

func (h *attachmentHandler) authorizeParticipant(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}
	if _, err := GetQuotationForUser(ctx.Param("id"), userID); err != nil {
		eq, externalErr := GetExternalQuotationByID(ctx.Param("id"))
		if externalErr != nil || eq.ArtisanID != userID {
			return responses.SendError(ctx, 404, "Quotation not found")
		}
	}
	return nil
}

func validAttachment(name, mime string, size, max int64) bool {
	if strings.TrimSpace(name) == "" || size <= 0 || (max > 0 && size > max) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "application/pdf", "image/jpeg", "image/png", "application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return true
	default:
		return false
	}
}

func safeName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	return strings.TrimSpace(name)
}

// Kept behind a small function so attachment handlers remain easy to test.
func configsCollection() *mongo.Collection {
	return configs.MI.DB.Collection("quotation_attachments")
}
