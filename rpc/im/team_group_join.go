package main

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *imServer) JoinTeamGroup(ctx context.Context, req *pb.JoinTeamGroupRequest) (*pb.JoinTeamGroupResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group joining is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or group ID")
	}
	userID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	teamCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorization))
	membership, err := s.teamClient.CheckTeamMember(teamCtx, &userpb.CheckTeamMemberRequest{TeamId: req.GetTeamId()})
	if err != nil {
		switch status.Code(err) {
		case codes.PermissionDenied, codes.Unauthenticated, codes.DeadlineExceeded, codes.Canceled:
			return nil, err
		default:
			return nil, status.Error(codes.Unavailable, "team membership check unavailable")
		}
	}
	if err := validateTeamGroupAuthorization(userID, membership.GetUserId(), membership.GetGeneration()); err != nil {
		return nil, err
	}
	var group struct{ ID int64 }
	err = s.db.WithContext(ctx).Table("groups").Select("id").
		Where("id = ? AND team_id = ?", req.GetGroupId(), req.GetTeamId()).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "team group not found")
	}
	if err != nil {
		return nil, joinTeamGroupDBError(ctx, err)
	}
	member := struct {
		GroupID int64
		UserID  int64
		Role    int8
	}{req.GetGroupId(), userID, 0}
	err = withTeamGroupGeneration(ctx, s.db, req.GetTeamId(), userID, membership.GetGeneration(), func(tx *gorm.DB) error {
		insertErr := tx.Table("group_members").Create(&member).Error
		if insertErr == nil {
			return nil
		}
		var mysqlErr *mysql.MySQLError
		if errors.As(insertErr, &mysqlErr) && mysqlErr.Number == 1062 {
			var existing struct{ GroupID int64 }
			// Keep the generation lock through duplicate verification and commit.
			if err := tx.Table("group_members").Select("group_id").
				Where("group_id = ? AND user_id = ?", req.GetGroupId(), userID).Take(&existing).Error; err != nil {
				return err
			}
			if existing.GroupID != req.GetGroupId() {
				return status.Error(codes.Unavailable, "team group membership data unavailable")
			}
			return nil
		}
		return insertErr
	})
	if err != nil {
		if status.Code(err) == codes.Unavailable {
			return nil, joinTeamGroupDBError(ctx, err)
		}
		return nil, err
	}
	return &pb.JoinTeamGroupResponse{}, nil
}

func joinTeamGroupDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	logx.WithContext(ctx).Errorf("join team group failed: %v", err)
	return status.Error(codes.Unavailable, "IM database unavailable")
}
