package model

import "time"

// IMBot is owned by IM. It has no password, login token, or user membership.
type IMBot struct {
	ID          int64     `gorm:"primaryKey"`
	Code        string    `gorm:"size:32;not null;uniqueIndex:uk_im_bots_code"`
	DisplayName string    `gorm:"size:64;not null"`
	Status      int8      `gorm:"not null;default:1"` // 1: enabled, 2: disabled
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

func (IMBot) TableName() string { return "im_bots" }
