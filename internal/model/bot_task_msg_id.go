package model

import (
	"errors"
	"strconv"
)

// BotTaskItemMsgID preserves the original single-item ID for index 0.
// Every other stable collection index has its own durable deduplication key.
func BotTaskItemMsgID(runID int64, itemIndex int32) (string, error) {
	if runID <= 0 || itemIndex < 0 || itemIndex >= 5 {
		return "", errors.New("invalid bot task item identity")
	}
	id := BotTaskMsgIDPrefix + strconv.FormatInt(runID, 10)
	if itemIndex > 0 {
		id += ":" + strconv.FormatInt(int64(itemIndex), 10)
	}
	return id, nil
}
