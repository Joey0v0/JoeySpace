package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/pkg/errcode"
)

// Response 统一响应结构
type Response struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// Success 成功响应
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code: errcode.Success,
		Msg:  errcode.GetMsg(errcode.Success),
		Data: data,
	})
}

// Error 错误响应
func Error(c *gin.Context, code int) {
	c.JSON(http.StatusOK, Response{
		Code: code,
		Msg:  errcode.GetMsg(code),
	})
}

// ErrorWithMsg 带自定义消息的错误响应
func ErrorWithMsg(c *gin.Context, code int, msg string) {
	c.JSON(http.StatusOK, Response{
		Code: code,
		Msg:  msg,
	})
}
