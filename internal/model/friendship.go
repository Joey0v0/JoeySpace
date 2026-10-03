package model

import "time"

// Friendship 好友关系表
type Friendship struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"not null;uniqueIndex:uk_user_friend" json:"user_id"`
	FriendID  int64     `gorm:"not null;uniqueIndex:uk_user_friend;index:idx_friend_id" json:"friend_id"`
	Status    int8      `gorm:"not null;default:0" json:"status"` // 0:待确认 1:已接受
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

func (Friendship) TableName() string {
	return "friendships"
}
