package service

import (
	"errors"
	"strconv"

	"github.com/yjydist/go-im/internal/pkg/errcode"
)

// ErrBusiness 业务错误，携带错误码
// 用法：fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrUserNotFound)
var ErrBusiness = errors.New("business error")

// ParseBusinessError 从错误中解析业务错误码
// 返回错误码和是否为业务错误
func ParseBusinessError(err error) (int, bool) {
	if err == nil {
		return errcode.Success, false
	}
	// 尝试解析 "business error: 20001" 格式
	if errors.Is(err, ErrBusiness) {
		msg := err.Error()
		// 提取冒号后的数字
		for i := len(msg) - 1; i >= 0; i-- {
			if msg[i] == ' ' {
				codeStr := msg[i+1:]
				if code, parseErr := strconv.Atoi(codeStr); parseErr == nil {
					return code, true
				}
				break
			}
		}
		return errcode.ErrInternal, true
	}
	return errcode.ErrInternal, false
}
