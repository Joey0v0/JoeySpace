package model

import "time"

// User 用户表
type User struct {
	ID        int64     `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:32;not null;uniqueIndex:idx_username" json:"username"`
	Password  string    `gorm:"size:128;not null" json:"-"`
	Nickname  string    `gorm:"size:64;not null;default:''" json:"nickname"`
	Avatar    string    `gorm:"size:256;not null;default:''" json:"avatar"`
	Status    int8      `gorm:"not null;default:1" json:"status"` // 1:正常 2:封禁
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

func (User) TableName() string {
	return "users"
}
