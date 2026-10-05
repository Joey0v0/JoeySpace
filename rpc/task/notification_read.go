package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MarkTaskNotificationRead fixes the first acknowledgement time on this recipient's row.
func (s *taskServer) MarkTaskNotificationRead(ctx context.Context, req *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error) {
	if s.db == nil || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "task notification read is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetNotificationId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid task notification read parameters")
	}
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	member, err := s.currentTeamMember(teamCtx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	var saved struct {
		NotificationID  int64
		CreatedAtUnixMs int64
		ReadAtUnixMs    int64
	}
	notFound := errors.New("notification outside recipient scope")
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rowQuery := func() *gorm.DB {
			return tx.Table("task_status_notifications").
				Select("id AS notification_id, CAST(UNIX_TIMESTAMP(created_at) * 1000 AS SIGNED) AS created_at_unix_ms, COALESCE(CAST(UNIX_TIMESTAMP(read_at) * 1000 AS SIGNED), 0) AS read_at_unix_ms").
				Where("id = ? AND team_id = ? AND recipient_id = ?", req.GetNotificationId(), req.GetTeamId(), member.GetUserId())
		}
		if err := rowQuery().Clauses(clause.Locking{Strength: "UPDATE"}).Take(&saved).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return notFound
			}
			return err
		}
		valid := func(requireRead bool) bool {
			return saved.NotificationID == req.GetNotificationId() && saved.CreatedAtUnixMs > 0 && saved.CreatedAtUnixMs <= maxTaskDueAtUnixMs &&
				saved.ReadAtUnixMs >= 0 && saved.ReadAtUnixMs <= maxTaskDueAtUnixMs &&
				(saved.ReadAtUnixMs == 0 && !requireRead || saved.ReadAtUnixMs >= saved.CreatedAtUnixMs)
		}
		if !valid(false) {
			return errors.New("invalid notification read record")
		}
		if saved.ReadAtUnixMs == 0 {
			updated := tx.Table("task_status_notifications").
				Where("id = ? AND team_id = ? AND recipient_id = ? AND read_at IS NULL", req.GetNotificationId(), req.GetTeamId(), member.GetUserId()).
				Update("read_at", gorm.Expr("CURRENT_TIMESTAMP"))
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errors.New("notification read update did not affect one row")
			}
			// Read back the database timestamp; do not guess it from the process clock.
			if err := rowQuery().Take(&saved).Error; err != nil {
				return err
			}
			if !valid(true) {
				return errors.New("invalid saved notification read record")
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, notFound) {
			return nil, status.Error(codes.NotFound, "task notification not found")
		}
		return nil, taskDatabaseError(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	// This check runs after commit. A denial does not undo the acknowledgement.
	current, err := s.currentTeamMember(teamCtx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if current.GetUserId() != member.GetUserId() {
		return nil, status.Error(codes.Unavailable, "team membership response invalid")
	}
	return &pb.MarkTaskNotificationReadResponse{NotificationId: saved.NotificationID, ReadAtUnixMs: saved.ReadAtUnixMs}, nil
}
