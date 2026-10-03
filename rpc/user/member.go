package main

import (
	"context"
	"errors"

	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) AddTeamMember(ctx context.Context, req *pb.AddTeamMemberRequest) (*pb.AddTeamMemberResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetUserId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or user ID")
	}

	operator, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var membership struct{ Role int8 }
	err = s.db.WithContext(ctx).Table("team_members").Select("role").
		Where("team_id = ? AND user_id = ?", req.GetTeamId(), operator.GetId()).Take(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && membership.Role != 2 {
		return nil, status.Error(codes.PermissionDenied, "team owner required")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}

	var target struct{ Status int8 }
	err = s.db.WithContext(ctx).Table("users").Select("status").Where("id = ?", req.GetUserId()).Take(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	if target.Status != 1 {
		return nil, status.Error(codes.FailedPrecondition, "user is disabled")
	}

	member := struct {
		TeamID int64
		UserID int64
		Role   int8
	}{req.GetTeamId(), req.GetUserId(), 0}
	if err := s.db.WithContext(ctx).Table("team_members").Create(&member).Error; err != nil {
		var mysqlErr *driver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return nil, status.Error(codes.AlreadyExists, "user is already a team member")
		}
		return nil, teamMemberDBError(ctx, err)
	}
	return &pb.AddTeamMemberResponse{}, nil
}

func teamMemberDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	logx.WithContext(ctx).Errorf("team database operation failed: %v", err)
	return status.Error(codes.Unavailable, "team database unavailable")
}
