package main

import (
	"context"
	"errors"

	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type teamLeaveIntent struct {
	ID         int64
	TeamID     int64
	UserID     int64
	Generation int64
	Status     int8
}

type teamLeaveMembershipRow struct {
	Role            int8  `gorm:"column:role"`
	MembershipState int8  `gorm:"column:membership_state"`
	Generation      int64 `gorm:"column:generation"`
}

type teamLeaveTeamRow struct {
	OwnerID int64 `gorm:"column:owner_id"`
}

type teamLeaveOperationRow struct {
	ID         int64  `gorm:"column:id;primaryKey;autoIncrement"`
	TeamID     int64  `gorm:"column:team_id"`
	UserID     int64  `gorm:"column:user_id"`
	RequestKey string `gorm:"column:request_key"`
	Generation int64  `gorm:"column:generation"`
	Status     int8   `gorm:"column:status"`
}

func beginTeamLeaveIntent(ctx context.Context, db *gorm.DB, teamID, actorID int64, requestKey string) (*teamLeaveIntent, error) {
	if err := teamLeaveContextError(ctx); err != nil {
		return nil, err
	}
	if teamID <= 0 || actorID <= 0 || !validTeamLeaveRequestKey(requestKey) {
		return nil, status.Error(codes.InvalidArgument, "invalid team leave parameters")
	}
	if db == nil {
		return nil, status.Error(codes.Unavailable, "team leave database unavailable")
	}

	var result *teamLeaveIntent
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var member teamLeaveMembershipRow
		err := tx.Table("team_members").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("role, membership_state, generation").
			Where("team_id = ? AND user_id = ?", teamID, actorID).Take(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Error(codes.PermissionDenied, "team membership required")
		}
		if err != nil {
			return err
		}
		if member.Generation <= 0 || member.Role < 0 || member.Role > 2 || member.MembershipState < model.TeamMembershipActive || member.MembershipState > model.TeamMembershipLeft {
			return status.Error(codes.Unavailable, "team membership data unavailable")
		}
		var team teamLeaveTeamRow
		err = tx.Table("teams").Select("owner_id").Where("id = ?", teamID).Take(&team).Error
		if err != nil || team.OwnerID <= 0 {
			return status.Error(codes.Unavailable, "team data unavailable")
		}
		if team.OwnerID == actorID {
			return status.Error(codes.FailedPrecondition, "team owner cannot leave")
		}

		var existing teamLeaveOperationRow
		err = tx.Table("user_team_leave_operations").Select("id, team_id, user_id, generation, status").
			Where("user_id = ? AND request_key = ?", actorID, requestKey).Take(&existing).Error
		if err == nil {
			if existing.ID <= 0 || existing.Status < 0 || existing.Status > 1 {
				return status.Error(codes.Unavailable, "team leave operation data unavailable")
			}
			if existing.TeamID != teamID || existing.UserID != actorID || existing.Generation != member.Generation {
				return status.Error(codes.AlreadyExists, "team leave request conflicts with current membership")
			}
			if existing.Status == 0 && member.MembershipState != model.TeamMembershipLeaving || existing.Status == 1 && member.MembershipState != model.TeamMembershipLeft {
				return status.Error(codes.FailedPrecondition, "team leave operation and membership differ")
			}
			result = &teamLeaveIntent{ID: existing.ID, TeamID: existing.TeamID, UserID: existing.UserID, Generation: existing.Generation, Status: existing.Status}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if member.Role == 2 {
			return status.Error(codes.FailedPrecondition, "team owner cannot leave")
		}
		if member.MembershipState != model.TeamMembershipActive {
			return status.Error(codes.FailedPrecondition, "team membership is not active")
		}

		update := tx.Table("team_members").Where("team_id = ? AND user_id = ? AND membership_state = ? AND generation = ? AND role IN ?", teamID, actorID, model.TeamMembershipActive, member.Generation, []int8{0, 1}).Update("membership_state", model.TeamMembershipLeaving)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return status.Error(codes.FailedPrecondition, "team membership changed; retry")
		}

		operation := teamLeaveOperationRow{TeamID: teamID, UserID: actorID, RequestKey: requestKey, Generation: member.Generation, Status: 0}
		if err := tx.Table("user_team_leave_operations").Create(&operation).Error; err != nil {
			var mysqlErr *driver.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
				return status.Error(codes.AlreadyExists, "team leave request conflicts with current membership")
			}
			return err
		}
		if operation.ID <= 0 {
			return status.Error(codes.Unavailable, "team leave operation unavailable")
		}
		result = &teamLeaveIntent{ID: operation.ID, TeamID: teamID, UserID: actorID, Generation: member.Generation, Status: 0}
		return nil
	})
	if err != nil {
		if ctxErr := teamLeaveContextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "team leave database unavailable")
	}
	return result, nil
}

func validTeamLeaveRequestKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func teamLeaveContextError(ctx context.Context) error {
	if ctx == nil {
		return status.Error(codes.InvalidArgument, "team leave context is required")
	}
	switch ctx.Err() {
	case context.Canceled:
		return status.Error(codes.Canceled, "team leave canceled")
	case context.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, "team leave deadline exceeded")
	default:
		return nil
	}
}
