package ws

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/yjydist/go-im/internal/model"
)

const maxSafeJSONInteger = 9007199254740991

// ClientMsg 客户端上行消息
type ClientMsg struct {
	Type string          `json:"type"` // "chat" (聊天), "ack" (已读确认), "ping" (心跳)
	Data json.RawMessage `json:"data"`
}

// ChatData 聊天消息数据
type ChatData struct {
	MsgID            string  `json:"msg_id"`
	ToID             int64   `json:"to_id"`
	ChatType         int     `json:"chat_type"`    // 1:单聊 2:群聊
	ContentType      int     `json:"content_type"` // 1:文本 2:图片 3:文件
	Content          string  `json:"content"`
	MentionedUserIDs []int64 `json:"mentioned_user_ids,omitempty"`
}

// UnmarshalJSON 接受十进制字符串 ID，并兼容不会在浏览器中丢精度的旧数字 ID。
func (d *ChatData) UnmarshalJSON(data []byte) error {
	var wire struct {
		MsgID            string          `json:"msg_id"`
		ToID             json.RawMessage `json:"to_id"`
		ChatType         int             `json:"chat_type"`
		ContentType      int             `json:"content_type"`
		Content          string          `json:"content"`
		MentionedUserIDs json.RawMessage `json:"mentioned_user_ids"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var id int64
	if len(wire.ToID) > 0 && wire.ToID[0] == '"' {
		var value string
		if err := json.Unmarshal(wire.ToID, &value); err != nil {
			return err
		}
		var err error
		id, err = strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
			return fmt.Errorf("invalid to_id string")
		}
	} else {
		var err error
		id, err = strconv.ParseInt(string(wire.ToID), 10, 64)
		if err != nil || id <= 0 || id > maxSafeJSONInteger {
			return fmt.Errorf("invalid or unsafe numeric to_id")
		}
	}
	var mentions []int64
	if len(wire.MentionedUserIDs) > 0 {
		if wire.MentionedUserIDs[0] != '[' {
			return fmt.Errorf("invalid mentioned_user_ids")
		}
		var values []json.RawMessage
		if err := json.Unmarshal(wire.MentionedUserIDs, &values); err != nil || len(values) > 10 {
			return fmt.Errorf("invalid mentioned_user_ids")
		}
		seen := make(map[int64]bool, len(values))
		for _, raw := range values {
			if len(raw) < 3 || raw[0] != '"' {
				return fmt.Errorf("invalid mentioned user ID")
			}
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return fmt.Errorf("invalid mentioned user ID")
			}
			mentioned, err := strconv.ParseInt(value, 10, 64)
			if err != nil || mentioned <= 0 || strconv.FormatInt(mentioned, 10) != value || seen[mentioned] {
				return fmt.Errorf("invalid mentioned user ID")
			}
			seen[mentioned] = true
			mentions = append(mentions, mentioned)
		}
		sort.Slice(mentions, func(i, j int) bool { return mentions[i] < mentions[j] })
	}
	*d = ChatData{MsgID: wire.MsgID, ToID: id, ChatType: wire.ChatType, ContentType: wire.ContentType, Content: wire.Content, MentionedUserIDs: mentions}
	return nil
}

// ServerMsg 服务端下行消息
type ServerMsg struct {
	Type string      `json:"type"` // "chat" (新消息), "ack" (服务端确认接收), "error" (错误信息)
	Data interface{} `json:"data"`
}

// AckData ACK 数据
type AckData struct {
	MsgID string `json:"msg_id"`
}

// ErrorData 错误数据
type ErrorData struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// PushMsg 内部推送消息（Push 服务 -> WS 网关）
type PushMsg struct {
	UserID int64           `json:"user_id"`
	Data   json.RawMessage `json:"data"`
}

// BrowserChatMessage 用字符串传递整数 ID，避免浏览器 JSON 解析时丢失精度。
type BrowserChatMessage struct {
	ID               string    `json:"id"`
	MsgID            string    `json:"msg_id"`
	FromID           string    `json:"from_id"`
	SenderType       int8      `json:"sender_type"`
	InitiatorID      string    `json:"initiator_id"`
	ToID             string    `json:"to_id"`
	ChatType         int8      `json:"chat_type"`
	ContentType      int8      `json:"content_type"`
	Content          string    `json:"content"`
	MentionedUserIDs []string  `json:"mentioned_user_ids,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func browserChatMessage(msg model.Message) BrowserChatMessage {
	senderType := msg.SenderType
	if senderType == 0 {
		senderType = model.MessageSenderUser
	}
	mentioned := make([]string, 0, len(msg.MentionedUserIDs))
	for _, id := range msg.MentionedUserIDs {
		if id > 0 {
			mentioned = append(mentioned, strconv.FormatInt(id, 10))
		}
	}
	return BrowserChatMessage{
		ID: strconv.FormatInt(msg.ID, 10), MsgID: msg.MsgID,
		FromID: strconv.FormatInt(msg.FromID, 10), ToID: strconv.FormatInt(msg.ToID, 10),
		SenderType: senderType, InitiatorID: strconv.FormatInt(msg.InitiatorID, 10),
		ChatType: msg.ChatType, ContentType: msg.ContentType,
		Content: msg.Content, MentionedUserIDs: mentioned, CreatedAt: msg.CreatedAt,
	}
}

// KafkaChatMsg Kafka 中传输的聊天消息
type KafkaChatMsg = model.ChatEvent
