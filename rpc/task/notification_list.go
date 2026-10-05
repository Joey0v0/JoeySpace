package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *taskServer) ListTaskNotifications(ctx context.Context, req *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
	if s.db == nil || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "task notification listing is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetBeforeNotificationId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid task notification list parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
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
	var rows []struct {
		NotificationID  int64
		TaskID          int64
		ActorID         int64
		FromStatus      int32
		ToStatus        int32
		CreatedAtUnixMs int64
		ReadAtUnixMs    int64
	}
	query := s.db.WithContext(ctx).Table("task_status_notifications AS n").
		Select("n.id AS notification_id, n.task_id, o.actor_id, o.from_status, o.to_status, CAST(UNIX_TIMESTAMP(n.created_at) * 1000 AS SIGNED) AS created_at_unix_ms, COALESCE(CAST(UNIX_TIMESTAMP(n.read_at) * 1000 AS SIGNED), 0) AS read_at_unix_ms").
		Joins("JOIN task_operations AS o ON o.id = n.operation_id AND o.task_id = n.task_id").
		Where("n.team_id = ? AND n.recipient_id = ?", req.GetTeamId(), member.GetUserId())
	if req.GetBeforeNotificationId() > 0 {
		query = query.Where("n.id < ?", req.GetBeforeNotificationId())
	}
	if err := query.Order("n.id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, taskDatabaseError(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if len(rows) > limit+1 {
		return nil, taskDatabaseError(ctx, errors.New("invalid task notification result size"))
	}
	for i, row := range rows {
		if row.NotificationID <= 0 || row.TaskID <= 0 || row.ActorID <= 0 || row.FromStatus < 0 || row.FromStatus > 2 || row.ToStatus < 0 || row.ToStatus > 2 || row.FromStatus == row.ToStatus || row.CreatedAtUnixMs <= 0 || row.CreatedAtUnixMs > maxTaskDueAtUnixMs || (req.GetBeforeNotificationId() > 0 && row.NotificationID >= req.GetBeforeNotificationId()) || (i > 0 && row.NotificationID >= rows[i-1].NotificationID) {
			return nil, taskDatabaseError(ctx, errors.New("invalid task notification result"))
		}
		if row.ReadAtUnixMs < 0 || row.ReadAtUnixMs > maxTaskDueAtUnixMs || (row.ReadAtUnixMs > 0 && row.ReadAtUnixMs < row.CreatedAtUnixMs) {
			return nil, taskDatabaseError(ctx, errors.New("invalid task notification read time"))
		}
	}
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
	result := &pb.ListTaskNotificationsResponse{Notifications: make([]*pb.TaskNotificationItem, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextBeforeNotificationId = rows[len(rows)-1].NotificationID
	}
	for _, row := range rows {
		result.Notifications = append(result.Notifications, &pb.TaskNotificationItem{
			NotificationId: row.NotificationID, TaskId: row.TaskID, ActorId: row.ActorID,
			FromStatus: row.FromStatus, ToStatus: row.ToStatus, CreatedAtUnixMs: row.CreatedAtUnixMs, ReadAtUnixMs: row.ReadAtUnixMs,
		})
	}
	return result, nil
}
