package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) SetTeamMemberRole(ctx context.Context, req *pb.SetTeamMemberRoleRequest) (*pb.SetTeamMemberRoleResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetUserId() <= 0 || req.GetRole() != 0 && req.GetRole() != 1 {
		return nil, status.Error(codes.InvalidArgument, "invalid team, user, or role")
	}

	operator, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var operatorMember struct{ Role int8 }
	err = s.db.WithContext(ctx).Table("team_members").Select("role").
		Where("team_id = ? AND user_id = ?", req.GetTeamId(), operator.GetId()).Take(&operatorMember).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && operatorMember.Role != 2 {
		return nil, status.Error(codes.PermissionDenied, "team owner required")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	if req.GetUserId() == operator.GetId() {
		return nil, status.Error(codes.FailedPrecondition, "cannot change owner role")
	}

	var target struct{ Role int8 }
	err = s.db.WithContext(ctx).Table("team_members").Select("role").
		Where("team_id = ? AND user_id = ?", req.GetTeamId(), req.GetUserId()).Take(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "team member not found")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	if target.Role == 2 {
		return nil, status.Error(codes.FailedPrecondition, "cannot change owner role")
	}
	if target.Role == int8(req.GetRole()) {
		return &pb.SetTeamMemberRoleResponse{}, nil
	}
	update := s.db.WithContext(ctx).Table("team_members").
		Where("team_id = ? AND user_id = ? AND role IN ?", req.GetTeamId(), req.GetUserId(), []int8{0, 1}).
		Update("role", req.GetRole())
	if update.Error != nil {
		return nil, teamMemberDBError(ctx, update.Error)
	}
	if update.RowsAffected != 1 {
		return nil, status.Error(codes.FailedPrecondition, "team membership changed; retry")
	}
	return &pb.SetTeamMemberRoleResponse{}, nil
}
