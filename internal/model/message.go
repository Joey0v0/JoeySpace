package model

import "time"

const (
	MessageSenderUser int8 = 1
	MessageSenderBot  int8 = 2
)

// Message 消息主表
type Message struct {
	ID          int64     `gorm:"primaryKey" json:"id"`
	MsgID       string    `gorm:"size:64;not null;uniqueIndex" json:"msg_id"` // 客户端生成的 UUID，防重幂等
	FromID      int64     `gorm:"not null" json:"from_id"`
	SenderType  int8      `gorm:"not null;default:1" json:"sender_type"`   // 1: user, 2: IM bot
	InitiatorID int64     `gorm:"not null;default:0" json:"initiator_id"`  // bot operation's authorizing user; 0 for user messages
	ToID        int64     `gorm:"not null;index:idx_to_time" json:"to_id"` // 接收人ID 或 群组ID
	ChatType    int8      `gorm:"not null" json:"chat_type"`               // 1:单聊 2:群聊
	ContentType int8      `gorm:"not null;default:1" json:"content_type"`  // 1:文本 2:图片 3:文件 4:任务创建卡片
	Content     string    `gorm:"type:text;not null" json:"content"`
	CreatedAt   time.Time `gorm:"autoCreateTime;index:idx_to_time" json:"created_at"`
}

func (Message) TableName() string {
	return "messages"
}

// OfflineMessage 离线消息表
type OfflineMessage struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"not null;index:idx_user_id;uniqueIndex:uk_offline_user_message" json:"user_id"` // 接收方ID
	MessageID int64     `gorm:"not null;uniqueIndex:uk_offline_user_message" json:"message_id"`                // 关联 messages 表的 ID
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

func (OfflineMessage) TableName() string {
	return "offline_messages"
}
