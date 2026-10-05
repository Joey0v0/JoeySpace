package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"go.uber.org/zap"
)

// RegisterRoutes 注册所有路由
func RegisterRoutes(r *gin.Engine, logger *zap.Logger, offlineClient OfflineMessagesClient) {
	userHandler := NewUserHandler(logger)
	friendHandler := NewFriendHandler(logger)
	groupHandler := NewGroupHandler(logger)
	msgHandler := NewMessageHandler(logger, offlineClient)

	// 公开接口
	v1 := r.Group("/api/v1")
	{
		v1.POST("/user/register", userHandler.Register)
		v1.POST("/user/login", userHandler.Login)
	}

	// 需要认证的接口
	auth := v1.Group("")
	auth.Use(middleware.Auth())
	{
		// 用户
		auth.GET("/user/info", userHandler.GetUserInfo)

		// 好友
		auth.POST("/friend/add", friendHandler.AddFriend)
		auth.POST("/friend/accept", friendHandler.AcceptFriend)
		auth.GET("/friend/list", friendHandler.ListFriends)

		// 群组
		auth.POST("/group/create", groupHandler.CreateGroup)
		auth.POST("/group/join", groupHandler.JoinGroup)
		auth.GET("/group/list", groupHandler.ListMyGroups)
		auth.GET("/group/members", groupHandler.ListMembers)

		// 消息
		auth.GET("/message/offline", msgHandler.GetOfflineMessages)
		auth.POST("/message/offline/ack", msgHandler.AckOfflineMessages)
		auth.GET("/message/history", msgHandler.GetHistory)
	}
}
