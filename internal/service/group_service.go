package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GroupService 群组服务接口
type GroupService interface {
	CreateGroup(ctx context.Context, name string, ownerID int64) (*model.Group, error)
	JoinGroup(ctx context.Context, groupID, userID int64) error
	ListMyGroups(ctx context.Context, userID int64) ([]model.Group, error)
	ListMembers(ctx context.Context, groupID, userID int64) ([]model.GroupMember, error)
}

type groupService struct {
	groupRepo repository.GroupRepository
	logger    *zap.Logger
}

// NewGroupService 创建群组服务
func NewGroupService(logger *zap.Logger) GroupService {
	return &groupService{
		groupRepo: repository.NewGroupRepository(),
		logger:    logger,
	}
}

func (s *groupService) CreateGroup(ctx context.Context, name string, ownerID int64) (*model.Group, error) {
	group := &model.Group{
		ID:      snowflake.GenID(),
		Name:    name,
		OwnerID: ownerID,
	}

	if err := s.groupRepo.CreateWithOwner(ctx, group); err != nil {
		return nil, err
	}

	s.logger.Info("group created",
		zap.Int64("group_id", group.ID),
		zap.String("name", name),
		zap.Int64("owner_id", ownerID),
	)
	return group, nil
}

func (s *groupService) JoinGroup(ctx context.Context, groupID, userID int64) error {
	// 检查群组是否存在
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrGroupNotFound)
		}
		return fmt.Errorf("query group failed: %w", err)
	}
	// 团队群不能通过旧的开放加入接口绕过团队成员资格校验。
	if group.TeamID != nil {
		return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrForbidden)
	}

	// 检查是否已在群中
	_, err = s.groupRepo.GetMember(ctx, groupID, userID)
	if err == nil {
		return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrGroupMember)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("query group member failed: %w", err)
	}

	member := &model.GroupMember{
		GroupID: groupID,
		UserID:  userID,
		Role:    0, // 普通成员
	}
	if err := s.groupRepo.AddMember(ctx, member); err != nil {
		return err
	}

	s.logger.Info("user joined group",
		zap.Int64("group_id", groupID),
		zap.Int64("user_id", userID),
	)
	return nil
}

func (s *groupService) ListMyGroups(ctx context.Context, userID int64) ([]model.Group, error) {
	return s.groupRepo.ListMyGroups(ctx, userID)
}

func (s *groupService) ListMembers(ctx context.Context, groupID, userID int64) ([]model.GroupMember, error) {
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrGroupNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("query group failed: %w", err)
	}
	// 团队群还没有完整的团队成员权限校验，不经旧入口展示成员。
	if group.TeamID != nil {
		return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrForbidden)
	}
	_, err = s.groupRepo.GetMember(ctx, groupID, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrGroupNotMember)
	}
	if err != nil {
		return nil, fmt.Errorf("query group member failed: %w", err)
	}
	return s.groupRepo.ListMembers(ctx, groupID)
}
