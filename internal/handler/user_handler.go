package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/pkg/response"
	"github.com/yjydist/go-im/internal/service"
	"go.uber.org/zap"
)

// UserHandler 用户 Handler
type UserHandler struct {
	userService service.UserService
	logger      *zap.Logger
}

// NewUserHandler 创建用户 Handler
func NewUserHandler(logger *zap.Logger) *UserHandler {
	return &UserHandler{
		userService: service.NewUserService(logger),
		logger:      logger,
	}
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required,min=6,max=64"`
	Nickname string `json:"nickname" binding:"max=64"`
}

// Register 用户注册
// @Summary 用户注册
// @Tags 用户
// @Accept json
// @Produce json
// @Param body body RegisterRequest true "注册信息"
// @Success 200 {object} response.Response
// @Router /api/v1/user/register [post]
func (h *UserHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, err.Error())
		return
	}

	if req.Nickname == "" {
		req.Nickname = req.Username
	}

	if err := h.userService.Register(c.Request.Context(), req.Username, req.Password, req.Nickname); err != nil {
		code, isBiz := service.ParseBusinessError(err)
		if isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("register failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, nil)
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	Token string `json:"token"`
}

// Login 用户登录
// @Summary 用户登录
// @Tags 用户
// @Accept json
// @Produce json
// @Param body body LoginRequest true "登录信息"
// @Success 200 {object} response.Response{data=LoginResponse}
// @Router /api/v1/user/login [post]
func (h *UserHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorWithMsg(c, errcode.ErrBadRequest, err.Error())
		return
	}

	token, err := h.userService.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		code, isBiz := service.ParseBusinessError(err)
		if isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("login failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, LoginResponse{Token: token})
}

// GetUserInfo 获取用户信息
// @Summary 获取用户信息
// @Tags 用户
// @Security Bearer
// @Produce json
// @Param user_id query int64 false "用户ID（不传则获取自己的信息）"
// @Success 200 {object} response.Response{data=model.User}
// @Failure 401 {object} response.Response
// @Router /api/v1/user/info [get]
func (h *UserHandler) GetUserInfo(c *gin.Context) {
	userID := middleware.GetUserID(c)

	// 如果指定了 user_id 参数，查询该用户信息
	if qID := c.Query("user_id"); qID != "" {
		var targetID int64
		if _, err := parseID(qID, &targetID); err != nil {
			response.ErrorWithMsg(c, errcode.ErrBadRequest, "invalid user_id")
			return
		}
		userID = targetID
	}

	user, err := h.userService.GetUserInfo(c.Request.Context(), userID)
	if err != nil {
		code, isBiz := service.ParseBusinessError(err)
		if isBiz {
			response.Error(c, code)
		} else {
			h.logger.Error("get user info failed", zap.Error(err))
			response.Error(c, errcode.ErrInternal)
		}
		return
	}

	response.Success(c, user)
}
