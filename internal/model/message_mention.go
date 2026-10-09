package model

// GroupMessageMention is persisted with a newly saved group message.
// Old messages intentionally have no inferred mention rows.
type GroupMessageMention struct {
	MessageID       int64 `gorm:"primaryKey;column:message_id"`
	GroupID         int64 `gorm:"not null;column:group_id"`
	MentionedUserID int64 `gorm:"primaryKey;column:mentioned_user_id"`
}

func (GroupMessageMention) TableName() string { return "im_group_message_mentions" }
