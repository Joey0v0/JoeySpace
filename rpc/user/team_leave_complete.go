package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The fixed scope always comes from the User-owned operation, never the IM response.
func (s *userServer) completeOwnTeamLeave(ctx context.Context, teamID int64, requestKey string) (*teamLeaveIntent, error) {
	if err := teamLeaveContextError(ctx); err != nil {
		return nil, err
	}
	if teamID <= 0 || !validTeamLeaveRequestKey(requestKey) {
		return nil, status.Error(codes.InvalidArgument, "invalid team leave parameters")
	}
	actor, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team leave database unavailable")
	}
	var op teamLeaveOperationRow
	err = s.db.WithContext(ctx).Table("user_team_leave_operations").Select("id, team_id, user_id, request_key, generation, status").
		Where("user_id = ? AND request_key = ?", actor.GetId(), requestKey).Take(&op).Error
	var intent *teamLeaveIntent
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Do not revoke membership when the cleanup service is not configured.
		if s.leaveIM == nil {
			return nil, status.Error(codes.Unavailable, "IM leave cleanup unavailable")
		}
		intent, err = beginTeamLeaveIntent(ctx, s.db, teamID, actor.GetId(), requestKey)
	} else if err == nil {
		if op.ID <= 0 || op.UserID != actor.GetId() || op.TeamID <= 0 || op.Generation <= 0 || op.RequestKey != requestKey || op.Status < 0 || op.Status > 1 {
			return nil, status.Error(codes.Unavailable, "team leave operation data unavailable")
		}
		if op.TeamID != teamID {
			return nil, status.Error(codes.AlreadyExists, "team leave request belongs to another team")
		}
		intent = &teamLeaveIntent{ID: op.ID, RequestKey: requestKey, TeamID: op.TeamID, UserID: op.UserID, Generation: op.Generation, Status: op.Status}
	} else {
		return nil, status.Error(codes.Unavailable, "team leave database unavailable")
	}
	if err != nil || intent.Status == 1 {
		return intent, err
	}
	var member teamLeaveMembershipRow
	err = s.db.WithContext(ctx).Table("team_members").Select("role, membership_state, generation").
		Where("team_id = ? AND user_id = ?", intent.TeamID, intent.UserID).Take(&member).Error
	if err != nil || member.Generation != intent.Generation || member.MembershipState != model.TeamMembershipLeaving {
		return nil, logTeamLeaveFailure(ctx, intent, "membership_verify", status.Error(codes.FailedPrecondition, "team leave operation and membership differ"))
	}
	if s.leaveIM == nil {
		return nil, logTeamLeaveFailure(ctx, intent, "im_cleanup", status.Error(codes.Unavailable, "IM leave cleanup unavailable"))
	}
	if err = s.leaveIM.CloseTeamGroupMemberships(ctx, intent.TeamID, intent.UserID, intent.Generation); err != nil {
		return nil, logTeamLeaveFailure(ctx, intent, "im_cleanup", err)
	}
	finished, err := finishTeamLeaveIntent(ctx, s.db, intent)
	if err != nil {
		return nil, logTeamLeaveFailure(ctx, intent, "user_finalize", err)
	}
	return finished, nil
}

// A pending operation can be retried by its owner. Keep its correlation fields
// in server logs without writing the request key, certificates or private error.
func logTeamLeaveFailure(ctx context.Context, intent *teamLeaveIntent, stage string, err error) error {
	if err != nil && intent != nil {
		logx.WithContext(ctx).Errorf("team_leave_failed operation_id=%d team_id=%d user_id=%d generation=%d stage=%s code=%s",
			intent.ID, intent.TeamID, intent.UserID, intent.Generation, stage, status.Code(err))
	}
	return err
}

func finishTeamLeaveIntent(ctx context.Context, db *gorm.DB, intent *teamLeaveIntent) (*teamLeaveIntent, error) {
	if err := teamLeaveContextError(ctx); err != nil {
		return nil, err
	}
	if db == nil || intent == nil || intent.ID <= 0 || intent.TeamID <= 0 || intent.UserID <= 0 || intent.Generation <= 0 || !validTeamLeaveRequestKey(intent.RequestKey) || intent.Status != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team leave operation")
	}
	var result *teamLeaveIntent
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Match beginTeamLeaveIntent's lock order: member first, operation second.
		var member teamLeaveMembershipRow
		if err := tx.Table("team_members").Clauses(clause.Locking{Strength: "UPDATE"}).Select("role, membership_state, generation").
			Where("team_id = ? AND user_id = ?", intent.TeamID, intent.UserID).Take(&member).Error; err != nil {
			return err
		}
		var op teamLeaveOperationRow
		if err := tx.Table("user_team_leave_operations").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id, team_id, user_id, request_key, generation, status").
			Where("id = ?", intent.ID).Take(&op).Error; err != nil {
			return err
		}
		if op.ID != intent.ID || op.TeamID != intent.TeamID || op.UserID != intent.UserID || op.RequestKey != intent.RequestKey || op.Generation != intent.Generation || op.Status < 0 || op.Status > 1 {
			return status.Error(codes.FailedPrecondition, "team leave operation changed")
		}
		if op.Status == 1 {
			// Another retry may have completed and even allowed a later rejoin.
			if member.Generation < op.Generation || member.Generation == op.Generation && member.MembershipState != model.TeamMembershipLeft {
				return status.Error(codes.FailedPrecondition, "team leave operation and membership differ")
			}
			result = &teamLeaveIntent{ID: op.ID, RequestKey: op.RequestKey, TeamID: op.TeamID, UserID: op.UserID, Generation: op.Generation, Status: 1}
			return nil
		}
		if member.Generation != op.Generation || member.MembershipState != model.TeamMembershipLeaving {
			return status.Error(codes.FailedPrecondition, "team leave operation and membership differ")
		}
		updated := tx.Table("team_members").Where("team_id = ? AND user_id = ? AND generation = ? AND membership_state = ?", op.TeamID, op.UserID, op.Generation, model.TeamMembershipLeaving).
			Update("membership_state", model.TeamMembershipLeft)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return status.Error(codes.FailedPrecondition, "team membership changed; retry")
		}
		updated = tx.Table("user_team_leave_operations").Where("id = ? AND team_id = ? AND user_id = ? AND generation = ? AND status = 0", op.ID, op.TeamID, op.UserID, op.Generation).
			Updates(map[string]interface{}{"status": int8(1), "completed_at": gorm.Expr("UTC_TIMESTAMP(6)")})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return status.Error(codes.FailedPrecondition, "team leave operation changed; retry")
		}
		result = &teamLeaveIntent{ID: op.ID, RequestKey: op.RequestKey, TeamID: op.TeamID, UserID: op.UserID, Generation: op.Generation, Status: 1}
		return nil
	})
	if err != nil {
		if ctxErr := teamLeaveContextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.FailedPrecondition, "team leave operation unavailable")
		}
		return nil, status.Error(codes.Unavailable, "team leave database unavailable")
	}
	return result, nil
}
