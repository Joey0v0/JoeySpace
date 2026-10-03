package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// CheckTeamGroupAccess checks current group and team membership plus exact
// group ownership. It returns no message content.
func (s *imServer) CheckTeamGroupAccess(ctx context.Context, req *pb.CheckTeamGroupAccessRequest) (*pb.CheckTeamGroupAccessResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group access check is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or group ID")
	}
	if _, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: req.GetGroupId()}); err != nil {
		return nil, err
	}
	var group struct{ TeamID *int64 }
	err := s.db.WithContext(ctx).Table("groups").Select("team_id").Where("id = ?", req.GetGroupId()).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && (group.TeamID == nil || *group.TeamID != req.GetTeamId()) {
		return nil, status.Error(codes.NotFound, "team group not found")
	}
	if err != nil {
		return nil, teamGroupDBError(ctx, err)
	}
	return &pb.CheckTeamGroupAccessResponse{}, nil
}
