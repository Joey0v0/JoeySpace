package main

import (
	"context"
	"errors"
	"math"

	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		Where("team_id = ? AND user_id = ? AND membership_state = ?", req.GetTeamId(), operator.GetId(), model.TeamMembershipActive).Take(&membership).Error
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

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var member teamLeaveMembershipRow
		err := tx.Table("team_members").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("role, membership_state, generation").
			Where("team_id = ? AND user_id = ?", req.GetTeamId(), req.GetUserId()).Take(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			newMember := struct {
				TeamID          int64
				UserID          int64
				Role            int8
				MembershipState int8
				Generation      int64
			}{req.GetTeamId(), req.GetUserId(), 0, model.TeamMembershipActive, 1}
			return tx.Table("team_members").Create(&newMember).Error
		}
		if err != nil {
			return err
		}
		if member.Generation <= 0 || member.Role < 0 || member.Role > 2 || member.MembershipState < model.TeamMembershipActive || member.MembershipState > model.TeamMembershipLeft {
			return status.Error(codes.Unavailable, "team membership data unavailable")
		}
		if member.MembershipState == model.TeamMembershipActive {
			return status.Error(codes.AlreadyExists, "user is already a team member")
		}
		if member.MembershipState == model.TeamMembershipLeaving {
			return status.Error(codes.FailedPrecondition, "team leave cleanup is pending")
		}
		if member.Role == 2 || member.Generation == math.MaxInt64 {
			return status.Error(codes.FailedPrecondition, "team membership cannot be reactivated")
		}
		var oldOperation teamLeaveOperationRow
		err = tx.Table("user_team_leave_operations").Select("status").
			Where("team_id = ? AND user_id = ? AND generation = ?", req.GetTeamId(), req.GetUserId(), member.Generation).
			Take(&oldOperation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && oldOperation.Status != 1 {
			return status.Error(codes.FailedPrecondition, "team leave cleanup is not completed")
		}
		if err != nil {
			return err
		}
		updated := tx.Table("team_members").Where("team_id = ? AND user_id = ? AND membership_state = ? AND generation = ?", req.GetTeamId(), req.GetUserId(), model.TeamMembershipLeft, member.Generation).
			Updates(map[string]interface{}{"role": int8(0), "membership_state": model.TeamMembershipActive, "generation": member.Generation + 1, "joined_at": gorm.Expr("CURRENT_TIMESTAMP")})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return status.Error(codes.FailedPrecondition, "team membership changed; retry")
		}
		return nil
	})
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
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
