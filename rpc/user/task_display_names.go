package main

import (
	"context"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// taskNameIDs validates the distinct bounded IDs without losing int64 precision.
func taskNameIDs(values []int64) ([]int64, error) {
	if len(values) == 0 {
		return nil, status.Error(codes.InvalidArgument, "expected 1 to 100 distinct positive IDs")
	}
	ids := make([]int64, 0, 100)
	seen := make(map[int64]struct{})
	for _, id := range values {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "IDs must be positive")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > 100 {
			return nil, status.Error(codes.InvalidArgument, "expected at most 100 distinct IDs")
		}
	}
	return ids, nil
}

func (s *userServer) BatchGetTeamMemberDisplayNames(ctx context.Context, req *pb.BatchGetTeamMemberDisplayNamesRequest) (*pb.BatchGetTeamMemberDisplayNamesResponse, error) {
	if req.GetTeamId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team ID")
	}
	ids, err := taskNameIDs(req.GetUserIds())
	if err != nil {
		return nil, err
	}
	if _, err = s.CheckTeamMember(ctx, &pb.CheckTeamMemberRequest{TeamId: req.GetTeamId()}); err != nil {
		return nil, err
	}
	var rows []struct {
		ID                 int64
		Username, Nickname string
	}
	err = s.db.WithContext(ctx).Table("team_members").Select("users.id, users.username, users.nickname").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ? AND team_members.membership_state = ? AND users.id IN ? AND users.status = ?", req.GetTeamId(), model.TeamMembershipActive, ids, 1).
		Order("users.id ASC").Find(&rows).Error
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	result := &pb.BatchGetTeamMemberDisplayNamesResponse{Users: make([]*pb.TeamMemberDisplayName, 0, len(rows))}
	for _, row := range rows {
		name := row.Nickname
		if name == "" {
			name = row.Username
		}
		result.Users = append(result.Users, &pb.TeamMemberDisplayName{UserId: row.ID, DisplayName: name})
	}
	return result, nil
}
