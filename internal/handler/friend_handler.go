package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/pkg/response"
	"github.com/yjydist/go-im/internal/service"
	"go.uber.org/zap"
)

// FriendHandler 好友 Handler
type FriendHandler struct {
	friendService service.FriendService
	logger        *zap.Logger
}

// NewFriendHandler 创建好友 Handler
func NewFriendHandler(logger *zap.Logger) *FriendHandler {
	return &FriendHandler{
		friendService: service.NewFriendService(logger),
		logger:        logger,
	}
}

// AddFriendRequest 添加好友请求
type AddFriendRequest struct {
	FriendID int64 `json:"friend_id" binding:"required"`
}

// AddFriend 发送好友申请
// @Summary 发送好友申请
// @Tags 好友
// @Security Bearer
// @Accept json
// @Produce json
// @Param body body AddFriendRequest true "好友ID"
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/friend/add [post]
func (h *FriendHandler) AddFriend(c *gin.Context) {
	var req AddFriendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.friendService.AddFriend(c.Request.Context(), userID, req.FriendID); err != nil {
		code, isBiz := service.ParseBusinessError(err)
		if isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("add friend failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, nil)
}

// AcceptFriendRequest 接受好友请求
type AcceptFriendRequest struct {
	FriendID int64 `json:"friend_id" binding:"required"`
}

// AcceptFriend 同意好友申请
// @Summary 同意好友申请
// @Tags 好友
// @Security Bearer
// @Accept json
// @Produce json
// @Param body body AcceptFriendRequest true "好友ID"
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/friend/accept [post]
func (h *FriendHandler) AcceptFriend(c *gin.Context) {
	var req AcceptFriendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.friendService.AcceptFriend(c.Request.Context(), userID, req.FriendID); err != nil {
		code, isBiz := service.ParseBusinessError(err)
		if isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("accept friend failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, nil)
}

// ListFriends 获取好友列表
// @Summary 获取好友列表
// @Tags 好友
// @Security Bearer
// @Produce json
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/friend/list [get]
func (h *FriendHandler) ListFriends(c *gin.Context) {
	userID := middleware.GetUserID(c)
	friends, err := h.friendService.ListFriends(c.Request.Context(), userID)
	if err != nil {
		h.logger.Error("list friends failed", zap.Error(err))
		response.Error(c, errcode.ErrInternal)
		return
	}

	response.Success(c, friends)
}
