package main

import (
	"context"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListMyTeams discovers only the authenticated member's current active teams.
func (s *userServer) ListMyTeams(ctx context.Context, req *pb.ListMyTeamsRequest) (*pb.ListMyTeamsResponse, error) {
	if req.GetAfterTeamId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid team list parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	member, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		TeamID int64
		Name   string
		Role   int8
	}
	err = s.db.WithContext(ctx).Table("team_members").
		Select("team_members.team_id AS team_id, teams.name, team_members.role").
		Joins("JOIN teams ON teams.id = team_members.team_id").
		Where("team_members.user_id = ? AND team_members.membership_state = ? AND team_members.team_id > ?", member.GetId(), model.TeamMembershipActive, req.GetAfterTeamId()).
		Order("team_members.team_id ASC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	result := &pb.ListMyTeamsResponse{Teams: make([]*pb.MyTeam, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextAfterTeamId = rows[len(rows)-1].TeamID
	}
	for _, row := range rows {
		result.Teams = append(result.Teams, &pb.MyTeam{TeamId: row.TeamID, Name: row.Name, Role: int32(row.Role)})
	}
	return result, nil
}
