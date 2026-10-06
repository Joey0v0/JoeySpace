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
		UserID     int64
		Role       int8
		Generation int64
	}
	err = s.db.WithContext(ctx).Table("team_members").Select("user_id, role, generation").
		Where("team_id = ? AND user_id = ? AND membership_state = ?", req.GetTeamId(), member.GetId(), model.TeamMembershipActive).Take(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.PermissionDenied, "team membership required")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	if membership.UserID <= 0 || membership.UserID != member.GetId() || membership.Role < 0 || membership.Role > 2 || membership.Generation <= 0 {
		return nil, status.Error(codes.Unavailable, "team membership data unavailable")
	}
	return &pb.CheckTeamMemberResponse{UserId: membership.UserID, Role: int32(membership.Role), Generation: membership.Generation}, nil
}
