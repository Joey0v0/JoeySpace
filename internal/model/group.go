package model

import "time"

// Group 群组表
type Group struct {
	ID        int64     `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	OwnerID   int64     `gorm:"not null" json:"owner_id"`
	TeamID    *int64    `gorm:"column:team_id" json:"team_id,omitempty"`
	Avatar    string    `gorm:"size:256;not null;default:''" json:"avatar"`
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

func (Group) TableName() string {
	return "groups"
}

// GroupMember 群成员表
type GroupMember struct {
	ID       int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	GroupID  int64     `gorm:"not null;uniqueIndex:uk_group_user" json:"group_id"`
	UserID   int64     `gorm:"not null;uniqueIndex:uk_group_user" json:"user_id"`
	Role     int8      `gorm:"not null;default:0" json:"role"` // 0:普通 1:管理员 2:群主
	JoinedAt time.Time `gorm:"autoCreateTime" json:"joined_at"`
}

func (GroupMember) TableName() string {
	return "group_members"
}
