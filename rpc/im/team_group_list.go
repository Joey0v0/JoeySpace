package main

import (
	"context"

	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *imServer) ListTeamGroups(ctx context.Context, req *pb.ListTeamGroupsRequest) (*pb.ListTeamGroupsResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group listing is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetAfterGroupId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid team group list parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	userID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	generation, err := s.directoryTeamGeneration(ctx, authorization, req.GetTeamId(), userID)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID      int64
		Name    string
		OwnerID int64
	}
	err = s.db.WithContext(ctx).Table("groups").Select("id, name, owner_id").
		Where("team_id = ? AND id > ?", req.GetTeamId(), req.GetAfterGroupId()).
		Order("id ASC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("list team groups failed: %v", err)
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	result := &pb.ListTeamGroupsResponse{Groups: make([]*pb.TeamGroup, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextAfterGroupId = rows[len(rows)-1].ID
	}
	for _, row := range rows {
		joined, err := s.directoryJoined(ctx, row.ID, req.GetTeamId(), userID, generation)
		if err != nil {
			return nil, err
		}
		result.Groups = append(result.Groups, &pb.TeamGroup{GroupId: row.ID, Name: row.Name, OwnerId: row.OwnerID, Joined: joined})
	}
	if err := s.directoryTeamUnchanged(ctx, authorization, req.GetTeamId(), userID, generation); err != nil {
		return nil, err
	}
	return result, nil
}
