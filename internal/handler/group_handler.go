package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/pkg/response"
	"github.com/yjydist/go-im/internal/service"
	"go.uber.org/zap"
)

// GroupHandler 群组 Handler
type GroupHandler struct {
	groupService service.GroupService
	logger       *zap.Logger
}

// NewGroupHandler 创建群组 Handler
func NewGroupHandler(logger *zap.Logger) *GroupHandler {
	return &GroupHandler{
		groupService: service.NewGroupService(logger),
		logger:       logger,
	}
}

// CreateGroupRequest 创建群组请求
type CreateGroupRequest struct {
	Name string `json:"name" binding:"required,min=1,max=64"`
}

// CreateGroup 创建群组
// @Summary 创建群组
// @Tags 群组
// @Security Bearer
// @Accept json
// @Produce json
// @Param body body CreateGroupRequest true "群组信息"
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/group/create [post]
func (h *GroupHandler) CreateGroup(c *gin.Context) {
	var req CreateGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	group, err := h.groupService.CreateGroup(c.Request.Context(), req.Name, userID)
	if err != nil {
		h.logger.Error("create group failed", zap.Error(err))
		response.Error(c, errcode.ErrInternal)
		return
	}

	response.Success(c, group)
}

// JoinGroupRequest 加入群组请求
type JoinGroupRequest struct {
	GroupID int64 `json:"group_id" binding:"required"`
}

// JoinGroup 加入群组
// @Summary 加入群组
// @Tags 群组
// @Security Bearer
// @Accept json
// @Produce json
// @Param body body JoinGroupRequest true "群组ID"
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/group/join [post]
func (h *GroupHandler) JoinGroup(c *gin.Context) {
	var req JoinGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.groupService.JoinGroup(c.Request.Context(), req.GroupID, userID); err != nil {
		code, isBiz := service.ParseBusinessError(err)
		if isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("join group failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, nil)
}

// ListMyGroups 获取我加入的群组列表
// @Summary 获取我加入的群组列表
// @Tags 群组
// @Security Bearer
// @Produce json
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/group/list [get]
func (h *GroupHandler) ListMyGroups(c *gin.Context) {
	userID := middleware.GetUserID(c)
	groups, err := h.groupService.ListMyGroups(c.Request.Context(), userID)
	if err != nil {
		h.logger.Error("list my groups failed", zap.Error(err))
		response.Error(c, errcode.ErrInternal)
		return
	}

	response.Success(c, groups)
}

// ListMembers 获取群成员列表
// @Summary 获取群成员列表
// @Tags 群组
// @Security Bearer
// @Produce json
// @Param group_id query int64 true "群组ID"
// @Success 200 {object} response.Response
// @Failure 401 {object} response.Response
// @Router /api/v1/group/members [get]
func (h *GroupHandler) ListMembers(c *gin.Context) {
	groupIDStr := c.Query("group_id")
	if groupIDStr == "" {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, "group_id is required")
		return
	}

	groupID, err := strconv.ParseInt(groupIDStr, 10, 64)
	if err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, "invalid group_id")
		return
	}

	members, err := h.groupService.ListMembers(c.Request.Context(), groupID, middleware.GetUserID(c))
	if err != nil {
		if code, isBiz := service.ParseBusinessError(err); isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("list group members failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, members)
}
