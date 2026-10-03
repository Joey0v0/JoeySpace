package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/internal/pkg/response"
)

const (
	// ContextKeyUserID 存储在 gin.Context 中的用户 ID key
	ContextKeyUserID = "user_id"
)

// Auth JWT 鉴权中间件
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 Header 获取 Authorization: Bearer <token>
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Error(c, errcode.ErrUnAuth)
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Error(c, errcode.ErrUnAuth)
			c.Abort()
			return
		}

		tokenString := parts[1]
		claims, err := pkgjwt.ParseToken(tokenString, config.GlobalConfig.JWT.Secret)
		if err != nil {
			if err == pkgjwt.ErrTokenExpired {
				response.Error(c, errcode.ErrTokenExpired)
			} else {
				response.Error(c, errcode.ErrTokenInvalid)
			}
			c.Abort()
			return
		}

		// 将 user_id 存入 context
		c.Set(ContextKeyUserID, claims.UserID)
		c.Next()
	}
}

// GetUserID 从 gin.Context 获取当前用户 ID
func GetUserID(c *gin.Context) int64 {
	userID, exists := c.Get(ContextKeyUserID)
	if !exists {
		return 0
	}
	return userID.(int64)
}
