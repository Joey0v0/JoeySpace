package model

import "time"

// GroupMessageRead records a user's first explicit reading of one message.
// ReadAt is assigned by MySQL; conflict retries must not update it.
type GroupMessageRead struct {
	UserID    int64     `gorm:"primaryKey;autoIncrement:false"`
	GroupID   int64     `gorm:"primaryKey;autoIncrement:false"`
	MessageID int64     `gorm:"primaryKey;autoIncrement:false"`
	ReadAt    time.Time `gorm:"->"`
}

func (GroupMessageRead) TableName() string { return "im_group_message_reads" }
