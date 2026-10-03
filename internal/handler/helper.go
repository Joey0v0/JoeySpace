package handler

import (
	"fmt"
	"strconv"
)

// parseID 解析字符串 ID 为 int64
func parseID(s string, id *int64) (int64, error) {
	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id: %s", s)
	}
	if id != nil {
		*id = val
	}
	return val, nil
}
