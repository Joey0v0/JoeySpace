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

// MessageService 消息服务接口
type MessageService interface {
	GetOfflineMessages(ctx context.Context, userID int64) ([]model.Message, error)
	AckOfflineMessages(ctx context.Context, userID int64, messageIDs []int64) error
	GetHistory(ctx context.Context, userID, targetID int64, chatType int8, cursorMsgID int64, limit int) ([]model.Message, error)
}

type messageService struct {
	msgRepo   repository.MessageRepository
	groupRepo repository.GroupRepository
	logger    *zap.Logger
}

// NewMessageService 创建消息服务
func NewMessageService(logger *zap.Logger) MessageService {
	return &messageService{
		msgRepo:   repository.NewMessageRepository(),
		groupRepo: repository.NewGroupRepository(),
		logger:    logger,
	}
}

func (s *messageService) GetOfflineMessages(ctx context.Context, userID int64) ([]model.Message, error) {
	messages, err := s.msgRepo.ListOffline(ctx, userID)
	if err != nil {
		return nil, err
	}
	s.logger.Info("offline messages pulled",
		zap.Int64("user_id", userID),
		zap.Int("count", len(messages)),
	)
	return messages, nil
}

func (s *messageService) AckOfflineMessages(ctx context.Context, userID int64, messageIDs []int64) error {
	if userID <= 0 || len(messageIDs) == 0 || len(messageIDs) > 1000 {
		return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrBadRequest)
	}
	for _, messageID := range messageIDs {
		if messageID <= 0 {
			return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrBadRequest)
		}
	}
	return s.msgRepo.DeleteOffline(ctx, userID, messageIDs)
}

func (s *messageService) GetHistory(ctx context.Context, userID, targetID int64, chatType int8, cursorMsgID int64, limit int) ([]model.Message, error) {
	if chatType == 2 {
		// 旧群聊也必须由业务层校验成员资格，不能只相信 HTTP 的 Token。
		if _, err := s.groupRepo.GetMember(ctx, targetID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrGroupNotMember)
			}
			return nil, fmt.Errorf("check group membership failed: %w", err)
		}
		group, err := s.groupRepo.GetByID(ctx, targetID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrGroupNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("query group failed: %w", err)
		}
		// 团队群历史需要同时校验团队资格；旧入口暂不开放。
		if group.TeamID != nil {
			return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrForbidden)
		}
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.msgRepo.GetHistory(ctx, userID, targetID, chatType, cursorMsgID, limit)
}
