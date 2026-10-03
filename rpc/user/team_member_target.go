package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// CheckTeamMemberByID only answers for a caller who is currently in the team.
func (s *userServer) CheckTeamMemberByID(ctx context.Context, req *pb.CheckTeamMemberByIDRequest) (*pb.CheckTeamMemberByIDResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetUserId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or user ID")
	}
	if _, err := s.CheckTeamMember(ctx, &pb.CheckTeamMemberRequest{TeamId: req.GetTeamId()}); err != nil {
		return nil, err
	}

	var target struct{ Status int8 }
	err := s.db.WithContext(ctx).Table("team_members").Select("users.status").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ? AND team_members.user_id = ?", req.GetTeamId(), req.GetUserId()).
		Take(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "team member not found")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	if target.Status != 1 {
		return nil, status.Error(codes.FailedPrecondition, "team member is disabled")
	}
	return &pb.CheckTeamMemberByIDResponse{}, nil
}
