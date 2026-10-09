package main

import (
	"context"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
)

func (s *userServer) BatchGetMyTeamNames(ctx context.Context, req *pb.BatchGetMyTeamNamesRequest) (*pb.BatchGetMyTeamNamesResponse, error) {
	ids, err := taskNameIDs(req.GetTeamIds())
	if err != nil {
		return nil, err
	}
	caller, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		TeamID int64
		Name   string
	}
	err = s.db.WithContext(ctx).Table("team_members").Select("teams.id AS team_id, teams.name").
		Joins("JOIN teams ON teams.id = team_members.team_id").
		Where("team_members.user_id = ? AND team_members.membership_state = ? AND teams.id IN ?", caller.GetId(), model.TeamMembershipActive, ids).
		Order("teams.id ASC").Find(&rows).Error
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	result := &pb.BatchGetMyTeamNamesResponse{Teams: make([]*pb.MyTeamName, 0, len(rows))}
	for _, row := range rows {
		result.Teams = append(result.Teams, &pb.MyTeamName{TeamId: row.TeamID, Name: row.Name})
	}
	return result, nil
}
