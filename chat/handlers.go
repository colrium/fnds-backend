package chat

import (
	"strings"

	"backend/contextutil"
	"backend/responses"

	"github.com/labstack/echo/v4"
)

func HandleListChats(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	jobRequestID := ctx.Param("jobRequestId")
	chats, err := ListChatsByJobRequest(jobRequestID)
	if err != nil {
		return responses.SendError(ctx, 500, "Failed to fetch chats")
	}
	return responses.SendOK(ctx, chats, "Chats retrieved successfully")
}

func HandleCreateChat(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	var req struct {
		JobRequestID string `json:"job_request_id"`
		SentTo       string `json:"sent_to"`
		Message      string `json:"message"`
		ChatType     string `json:"chat_type"`
	}
	if err := ctx.Bind(&req); err != nil {
		return responses.SendError(ctx, 400, "Invalid request body")
	}

	if strings.TrimSpace(req.JobRequestID) == "" {
		return responses.SendError(ctx, 400, "job_request_id is required")
	}
	if strings.TrimSpace(req.SentTo) == "" {
		return responses.SendError(ctx, 400, "sent_to is required")
	}

	if err := ValidateCreateChat(req.Message, req.ChatType); err != nil {
		return responses.SendError(ctx, 400, err.Error())
	}

	chat := Chat{
		JobRequestID: req.JobRequestID,
		SentBy:       userID,
		SentTo:       req.SentTo,
		Message:      req.Message,
		ChatType:     req.ChatType,
	}
	if err := CreateChat(&chat); err != nil {
		return responses.SendError(ctx, 500, "Failed to send message")
	}
	return responses.SendCreated(ctx, chat, "Message sent successfully")
}
