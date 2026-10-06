package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/yjydist/go-im/internal/model"
	"gorm.io/gorm"
)

// GroupRepository 群组数据访问接口
type GroupRepository interface {
	CreateWithOwner(ctx context.Context, group *model.Group) error
	GetByID(ctx context.Context, id int64) (*model.Group, error)
	AddMember(ctx context.Context, member *model.GroupMember) error
	GetMember(ctx context.Context, groupID, userID int64) (*model.GroupMember, error)
	ListMembers(ctx context.Context, groupID int64) ([]model.GroupMember, error)
	ListMemberIDs(ctx context.Context, groupID int64) ([]int64, error)
	CheckTeamGroupMemberGeneration(ctx context.Context, groupID, teamID, userID, generation int64) (bool, error)
	ListMyGroups(ctx context.Context, userID int64) ([]model.Group, error)
}

type groupRepository struct {
	db *gorm.DB
}

// NewGroupRepository 创建群组 Repository
func NewGroupRepository() GroupRepository {
	return &groupRepository{db: DB}
}

func (r *groupRepository) CreateWithOwner(ctx context.Context, group *model.Group) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 创建群组
		if err := tx.Create(group).Error; err != nil {
			return fmt.Errorf("create group failed: %w", err)
		}
		// 插入群主为成员
		member := &model.GroupMember{
			GroupID: group.ID,
			UserID:  group.OwnerID,
			Role:    2, // 群主
		}
		if err := tx.Create(member).Error; err != nil {
			return fmt.Errorf("add owner to group failed: %w", err)
		}
		return nil
	})
}

func (r *groupRepository) GetByID(ctx context.Context, id int64) (*model.Group, error) {
	var group model.Group
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&group).Error
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func (r *groupRepository) AddMember(ctx context.Context, member *model.GroupMember) error {
	if err := r.db.WithContext(ctx).Create(member).Error; err != nil {
		return fmt.Errorf("add group member failed: %w", err)
	}
	return nil
}

func (r *groupRepository) GetMember(ctx context.Context, groupID, userID int64) (*model.GroupMember, error) {
	var member model.GroupMember
	err := r.db.WithContext(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		First(&member).Error
	if err != nil {
		return nil, err
	}
	return &member, nil
}

func (r *groupRepository) ListMembers(ctx context.Context, groupID int64) ([]model.GroupMember, error) {
	var members []model.GroupMember
	err := r.db.WithContext(ctx).Where("group_id = ?", groupID).Find(&members).Error
	if err != nil {
		return nil, fmt.Errorf("list group members failed: %w", err)
	}
	return members, nil
}

func (r *groupRepository) ListMemberIDs(ctx context.Context, groupID int64) ([]int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).
		Model(&model.GroupMember{}).
		Where("group_id = ?", groupID).
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list group member ids failed: %w", err)
	}
	return ids, nil
}

// CheckTeamGroupMemberGeneration rechecks current IM membership and the
// permanent closure fence after User has returned an active generation.
func (r *groupRepository) CheckTeamGroupMemberGeneration(ctx context.Context, groupID, teamID, userID, generation int64) (bool, error) {
	if ctx == nil || groupID <= 0 || teamID <= 0 || userID <= 0 || generation <= 0 {
		return false, fmt.Errorf("invalid team group delivery scope")
	}
	const query = "SELECT gm.group_id, f.closed_through_generation FROM group_members AS gm " +
		"JOIN `groups` AS g ON g.id = gm.group_id " +
		"LEFT JOIN im_team_group_fences AS f ON f.team_id = g.team_id AND f.user_id = gm.user_id " +
		"WHERE gm.group_id = ? AND gm.user_id = ? AND g.team_id = ? LIMIT 2"
	rows, err := r.db.WithContext(ctx).Raw(query, groupID, userID, teamID).Rows()
	if err != nil {
		return false, fmt.Errorf("check team group delivery membership: %w", err)
	}
	defer rows.Close()
	found := false
	var closed int64
	for rows.Next() {
		var actualGroup int64
		var stored sql.NullInt64
		if found || rows.Scan(&actualGroup, &stored) != nil || actualGroup != groupID || stored.Valid && stored.Int64 < 0 {
			return false, fmt.Errorf("invalid team group delivery membership row")
		}
		if stored.Valid {
			closed = stored.Int64
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read team group delivery membership: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return found && generation > closed, nil
}

func (r *groupRepository) ListMyGroups(ctx context.Context, userID int64) ([]model.Group, error) {
	var groups []model.Group
	err := r.db.WithContext(ctx).
		Table("groups").
		Joins("JOIN group_members ON group_members.group_id = groups.id").
		Where("group_members.user_id = ? AND groups.team_id IS NULL", userID).
		Find(&groups).Error
	if err != nil {
		return nil, fmt.Errorf("list my groups failed: %w", err)
	}
	return groups, nil
}
