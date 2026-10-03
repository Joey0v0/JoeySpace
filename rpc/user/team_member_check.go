package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) CheckTeamMember(ctx context.Context, req *pb.CheckTeamMemberRequest) (*pb.CheckTeamMemberResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	if req.GetTeamId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team ID")
	}

	member, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var membership struct {
		UserID int64
		Role   int8
	}
	err = s.db.WithContext(ctx).Table("team_members").Select("user_id, role").
		Where("team_id = ? AND user_id = ?", req.GetTeamId(), member.GetId()).Take(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.PermissionDenied, "team membership required")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	return &pb.CheckTeamMemberResponse{UserId: membership.UserID, Role: int32(membership.Role)}, nil
}
