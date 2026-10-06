package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) ListTeamMembers(ctx context.Context, req *pb.ListTeamMembersRequest) (*pb.ListTeamMembersResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetAfterUserId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid member list parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}

	operator, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var membership struct{ UserID int64 }
	err = s.db.WithContext(ctx).Table("team_members").Select("user_id").
		Where("team_id = ? AND user_id = ? AND membership_state = ?", req.GetTeamId(), operator.GetId(), model.TeamMembershipActive).Take(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.PermissionDenied, "team membership required")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}

	var rows []struct {
		UserID   int64
		Username string
		Nickname string
		Role     int8
	}
	err = s.db.WithContext(ctx).Table("team_members").
		Select("team_members.user_id AS user_id, users.username, users.nickname, team_members.role").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ? AND team_members.user_id > ? AND team_members.membership_state = ?", req.GetTeamId(), req.GetAfterUserId(), model.TeamMembershipActive).
		Order("team_members.user_id ASC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}

	result := &pb.ListTeamMembersResponse{Members: make([]*pb.TeamMember, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextAfterUserId = rows[len(rows)-1].UserID
	}
	for _, row := range rows {
		result.Members = append(result.Members, &pb.TeamMember{
			UserId: row.UserID, Username: row.Username, Nickname: row.Nickname, Role: int32(row.Role),
		})
	}
	return result, nil
}
