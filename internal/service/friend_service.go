package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// FriendService 好友服务接口
type FriendService interface {
	AddFriend(ctx context.Context, userID, friendID int64) error
	AcceptFriend(ctx context.Context, userID, friendID int64) error
	ListFriends(ctx context.Context, userID int64) ([]model.User, error)
}

type friendService struct {
	friendRepo repository.FriendRepository
	userRepo   repository.UserRepository
	logger     *zap.Logger
}

// NewFriendService 创建好友服务
func NewFriendService(logger *zap.Logger) FriendService {
	return &friendService{
		friendRepo: repository.NewFriendRepository(),
		userRepo:   repository.NewUserRepository(),
		logger:     logger,
	}
}

func (s *friendService) AddFriend(ctx context.Context, userID, friendID int64) error {
	// 不能添加自己
	if userID == friendID {
		return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrFriendSelf)
	}

	// 检查对方是否存在
	_, err := s.userRepo.GetByID(ctx, friendID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrUserNotFound)
		}
		return fmt.Errorf("query friend user failed: %w", err)
	}

	// 检查是否已存在好友关系
	_, err = s.friendRepo.GetByUserAndFriend(ctx, userID, friendID)
	if err == nil {
		return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrFriendExist)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query friendship failed: %w", err)
	}

	friendship := &model.Friendship{
		UserID:   userID,
		FriendID: friendID,
		Status:   0, // 待确认
	}
	if err := s.friendRepo.Create(ctx, friendship); err != nil {
		return err
	}

	s.logger.Info("friend request sent",
		zap.Int64("user_id", userID),
		zap.Int64("friend_id", friendID),
	)
	return nil
}

func (s *friendService) AcceptFriend(ctx context.Context, userID, friendID int64) error {
	// 查找对方发来的好友申请
	_, err := s.friendRepo.GetByUserAndFriend(ctx, friendID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrFriendNotFound)
		}
		return fmt.Errorf("query friendship failed: %w", err)
	}

	// 更新状态为已接受
	if err := s.friendRepo.Accept(ctx, friendID, userID); err != nil {
		return err
	}

	// 同时创建反向好友关系（双向）
	reverseFriendship := &model.Friendship{
		UserID:   userID,
		FriendID: friendID,
		Status:   1, // 已接受
	}
	if err := s.friendRepo.Create(ctx, reverseFriendship); err != nil {
		// 如果反向关系已存在，忽略
		s.logger.Warn("create reverse friendship failed", zap.Error(err))
	}

	s.logger.Info("friend request accepted",
		zap.Int64("user_id", userID),
		zap.Int64("friend_id", friendID),
	)
	return nil
}

func (s *friendService) ListFriends(ctx context.Context, userID int64) ([]model.User, error) {
	return s.friendRepo.ListFriends(ctx, userID)
}
