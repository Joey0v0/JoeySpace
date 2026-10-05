package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/pkg/response"
	"github.com/yjydist/go-im/internal/service"
	"go.uber.org/zap"
)

// MessageHandler 消息 Handler
type MessageHandler struct {
	msgService    service.MessageService
	logger        *zap.Logger
	offlineClient OfflineMessagesClient
}

type offlineMessageResponse struct {
	ID          int64     `json:"id,string"`
	MsgID       string    `json:"msg_id"`
	FromID      int64     `json:"from_id,string"`
	SenderType  int8      `json:"sender_type"`
	InitiatorID int64     `json:"initiator_id,string"`
	ToID        int64     `json:"to_id,string"`
	ChatType    int8      `json:"chat_type"`
	ContentType int8      `json:"content_type"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

type offlineAckRequest struct {
	MessageIDs []string `json:"message_ids"`
}

// NewMessageHandler 创建消息 Handler
func NewMessageHandler(logger *zap.Logger, offlineClient OfflineMessagesClient) *MessageHandler {
	return &MessageHandler{
		msgService:    service.NewMessageService(logger),
		logger:        logger,
		offlineClient: offlineClient,
	}
}

// GetOfflineMessages 拉取离线消息
// @Summary 拉取离线消息
// @Tags 消息
// @Security Bearer
// @Produce json
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/message/offline [get]
func (h *MessageHandler) GetOfflineMessages(c *gin.Context) {
	userID := middleware.GetUserID(c)
	messages, err := h.msgService.GetOfflineMessages(c.Request.Context(), userID)
	if err != nil {
		h.logger.Error("get offline messages failed", zap.Error(err))
		response.Error(c, errcode.ErrInternal)
		return
	}

	result := make([]offlineMessageResponse, 0, len(messages))
	for _, message := range messages {
		senderType := message.SenderType
		if senderType == 0 {
			senderType = 1
		}
		result = append(result, offlineMessageResponse{
			ID: message.ID, MsgID: message.MsgID, FromID: message.FromID, ToID: message.ToID,
			SenderType: senderType, InitiatorID: message.InitiatorID,
			ChatType: message.ChatType, ContentType: message.ContentType, Content: message.Content, CreatedAt: message.CreatedAt,
		})
	}
	response.Success(c, result)
}

// AckOfflineMessages 客户端处理完成后确认离线消息。
func (h *MessageHandler) AckOfflineMessages(c *gin.Context) {
	var req offlineAckRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.MessageIDs) == 0 || len(req.MessageIDs) > 1000 {
		response.Error(c, errcode.ErrBadRequest)
		return
	}
	messageIDs := make([]int64, 0, len(req.MessageIDs))
	for _, rawID := range req.MessageIDs {
		messageID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || messageID <= 0 {
			response.Error(c, errcode.ErrBadRequest)
			return
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := h.msgService.AckOfflineMessages(c.Request.Context(), middleware.GetUserID(c), messageIDs); err != nil {
		if code, ok := service.ParseBusinessError(err); ok {
			response.Error(c, code)
			return
		}
		h.logger.Error("ack offline messages failed", zap.Error(err))
		response.Error(c, errcode.ErrInternal)
		return
	}
	response.Success(c, nil)
}

// GetHistory 拉取历史消息
// @Summary 游标分页拉取历史消息
// @Tags 消息
// @Security Bearer
// @Produce json
// @Param target_id query int64 true "目标ID（用户ID或群组ID）"
// @Param chat_type query int true "聊天类型 1:单聊 2:群聊"
// @Param cursor_msg_id query int64 false "游标消息ID"
// @Param limit query int false "每页数量（默认20，最大100）"
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/message/history [get]
func (h *MessageHandler) GetHistory(c *gin.Context) {
	userID := middleware.GetUserID(c)

	targetIDStr := c.Query("target_id")
	if targetIDStr == "" {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, "target_id is required")
		return
	}
	targetID, err := strconv.ParseInt(targetIDStr, 10, 64)
	if err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, "invalid target_id")
		return
	}

	chatTypeStr := c.Query("chat_type")
	if chatTypeStr == "" {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, "chat_type is required")
		return
	}
	chatType, err := strconv.Atoi(chatTypeStr)
	if err != nil || (chatType != 1 && chatType != 2) {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, "invalid chat_type, must be 1 or 2")
		return
	}

	var cursorMsgID int64
	if cursorStr := c.Query("cursor_msg_id"); cursorStr != "" {
		cursorMsgID, err = strconv.ParseInt(cursorStr, 10, 64)
		if err != nil {
			response.ErrorWithMsg(c, errcode.ErrBadRequest, "invalid cursor_msg_id")
			return
		}
	}

	limit := 20
	if limitStr := c.Query("limit"); limitStr != "" {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			response.ErrorWithMsg(c, errcode.ErrBadRequest, "invalid limit")
			return
		}
	}

	messages, err := h.msgService.GetHistory(c.Request.Context(), userID, targetID, int8(chatType), cursorMsgID, limit)
	if err != nil {
		if code, ok := service.ParseBusinessError(err); ok {
			response.Error(c, code)
			return
		}
		h.logger.Error("get history failed", zap.Error(err))
		response.Error(c, errcode.ErrInternal)
		return
	}

	response.Success(c, messages)
}
