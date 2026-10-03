package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"gorm.io/gorm"
)

// UserRepository 用户数据访问接口
type UserRepository interface {
	Create(ctx context.Context, user *model.User) error
	GetByUsername(ctx context.Context, username string) (*model.User, error)
	GetByID(ctx context.Context, id int64) (*model.User, error)
}

type userRepository struct {
	db *gorm.DB
}

// NewUserRepository 创建用户 Repository
func NewUserRepository() UserRepository {
	return &userRepository{db: DB}
}

func (r *userRepository) Create(ctx context.Context, user *model.User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		return fmt.Errorf("create user failed: %w", err)
	}
	return nil
}

func (r *userRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) GetByID(ctx context.Context, id int64) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FriendRepository 好友数据访问接口
type FriendRepository interface {
	Create(ctx context.Context, friendship *model.Friendship) error
	Accept(ctx context.Context, userID, friendID int64) error
	GetByUserAndFriend(ctx context.Context, userID, friendID int64) (*model.Friendship, error)
	ListFriends(ctx context.Context, userID int64) ([]model.User, error)
}

type friendRepository struct {
	db *gorm.DB
}

// NewFriendRepository 创建好友 Repository
func NewFriendRepository() FriendRepository {
	return &friendRepository{db: DB}
}

func (r *friendRepository) Create(ctx context.Context, friendship *model.Friendship) error {
	if err := r.db.WithContext(ctx).Create(friendship).Error; err != nil {
		return fmt.Errorf("create friendship failed: %w", err)
	}
	return nil
}

func (r *friendRepository) Accept(ctx context.Context, userID, friendID int64) error {
	err := r.db.WithContext(ctx).
		Model(&model.Friendship{}).
		Where("user_id = ? AND friend_id = ? AND status = 0", userID, friendID).
		Update("status", 1).
		Update("updated_at", time.Now()).Error
	if err != nil {
		return fmt.Errorf("accept friendship failed: %w", err)
	}
	return nil
}

func (r *friendRepository) GetByUserAndFriend(ctx context.Context, userID, friendID int64) (*model.Friendship, error) {
	var friendship model.Friendship
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND friend_id = ?", userID, friendID).
		First(&friendship).Error
	if err != nil {
		return nil, err
	}
	return &friendship, nil
}

func (r *friendRepository) ListFriends(ctx context.Context, userID int64) ([]model.User, error) {
	var users []model.User
	// 查找所有已接受的好友关系，获取好友用户信息
	err := r.db.WithContext(ctx).
		Table("users").
		Joins("JOIN friendships ON friendships.friend_id = users.id").
		Where("friendships.user_id = ? AND friendships.status = 1", userID).
		Find(&users).Error
	if err != nil {
		return nil, fmt.Errorf("list friends failed: %w", err)
	}
	return users, nil
}
