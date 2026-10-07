package model

import "time"

// DirectMessageRead records the first explicit reading of an incoming message.
// MySQL supplies ReadAt; replay must preserve that original time.
type DirectMessageRead struct {
	UserID    int64     `gorm:"primaryKey;autoIncrement:false"`
	PeerID    int64     `gorm:"primaryKey;autoIncrement:false"`
	MessageID int64     `gorm:"primaryKey;autoIncrement:false"`
	ReadAt    time.Time `gorm:"->"`
}

func (DirectMessageRead) TableName() string { return "im_direct_message_reads" }
