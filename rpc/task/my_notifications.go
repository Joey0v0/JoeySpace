package main

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type myNotificationRow struct {
	NotificationID  int64
	TeamID          int64
	TaskID          int64
	TaskTitle       string
	CurrentStatus   int32
	ActorID         int64
	FromStatus      int32
	ToStatus        int32
	CreatedAtUnixMs int64
	ReadAtUnixMs    int64
}

func (s *taskServer) notificationAccess(ctx context.Context, teamID int64) (context.Context, int64, []int64, error) {
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	caller, err := s.identityClient.GetMyInfo(teamCtx, &userpb.GetMyInfoRequest{})
	if err != nil {
		return nil, 0, nil, taskTeamError(err)
	}
	if caller.GetId() <= 0 {
		return nil, 0, nil, status.Error(codes.Unavailable, "caller identity response invalid")
	}
	teams, err := s.personalTaskTeams(teamCtx, teamID, caller.GetId())
	return teamCtx, caller.GetId(), teams, err
}

func (s *taskServer) ListMyTaskNotifications(ctx context.Context, req *pb.ListMyTaskNotificationsRequest) (*pb.ListMyTaskNotificationsResponse, error) {
	if s.db == nil || s.teamClient == nil || s.identityClient == nil || s.teamDirectory == nil {
		return nil, status.Error(codes.Unavailable, "personal task notifications are not enabled")
	}
	if req == nil || req.GetTeamId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 50 || len(req.GetCursor()) > 2048 {
		return nil, status.Error(codes.InvalidArgument, "invalid notification parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	_, callerID, teams, err := s.notificationAccess(ctx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	result := &pb.ListMyTaskNotificationsResponse{Notifications: make([]*pb.TaskNotificationItem, 0)}
	cursor := notificationCursor{Version: 1, TeamID: req.GetTeamId(), Phase: "unread"}
	if req.GetCursor() != "" {
		cursor, err = decodeNotificationCursor(req.GetCursor(), req.GetTeamId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid notification cursor")
		}
	}
	if len(teams) > 0 && req.GetCursor() == "" {
		var upper struct{ UpperNotificationID sql.NullInt64 }
		if err := s.db.WithContext(ctx).Table("task_status_notifications AS n").Select("MAX(n.id) AS upper_notification_id").Where("n.recipient_id = ? AND n.team_id IN ?", callerID, teams).Scan(&upper).Error; err != nil {
			return nil, taskDatabaseError(ctx, err)
		}
		if upper.UpperNotificationID.Valid {
			cursor.UpperID = upper.UpperNotificationID.Int64
		}
	}
	if len(teams) > 0 && cursor.UpperID > 0 {
		countQuery := s.db.WithContext(ctx).Table("task_status_notifications AS n").Where("n.recipient_id = ? AND n.team_id IN ? AND n.read_at IS NULL", callerID, teams)
		if err := countQuery.Count(&result.UnreadCount).Error; err != nil {
			return nil, taskDatabaseError(ctx, err)
		}
		query := s.db.WithContext(ctx).Table("task_status_notifications AS n").Select("n.id AS notification_id, n.team_id, n.task_id, t.title AS task_title, t.status AS current_status, o.actor_id, o.from_status, o.to_status, CAST(UNIX_TIMESTAMP(n.created_at) * 1000 AS SIGNED) AS created_at_unix_ms, COALESCE(CAST(UNIX_TIMESTAMP(n.read_at) * 1000 AS SIGNED), 0) AS read_at_unix_ms").Joins("JOIN task_operations AS o ON o.id = n.operation_id AND o.task_id = n.task_id").Joins("JOIN tasks AS t ON t.id = n.task_id AND t.team_id = n.team_id").Where("n.recipient_id = ? AND n.team_id IN ? AND n.id <= ?", callerID, teams, cursor.UpperID)
		if req.GetCursor() != "" {
			if cursor.Phase == "unread" {
				query = query.Where("((n.read_at IS NULL AND n.id < ?) OR n.read_at IS NOT NULL)", cursor.LastID)
			} else {
				query = query.Where("n.read_at IS NOT NULL AND n.id < ?", cursor.LastID)
			}
		}
		var rows []myNotificationRow
		if err := query.Order("n.read_at IS NULL DESC,n.id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return nil, taskDatabaseError(ctx, err)
		}
		if len(rows) > limit+1 {
			return nil, taskDatabaseError(ctx, errors.New("invalid notification page size"))
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		seen := make(map[int64]struct{}, len(rows))
		allowed := make(map[int64]struct{}, len(teams))
		for _, team := range teams {
			allowed[team] = struct{}{}
		}
		seenRead := false
		var previousUnread, previousRead int64
		for _, row := range rows {
			_, teamAllowed := allowed[row.TeamID]
			if !teamAllowed || row.NotificationID <= 0 || row.NotificationID > cursor.UpperID || row.TeamID <= 0 || row.TaskID <= 0 || row.ActorID <= 0 || row.TaskTitle == "" || row.CurrentStatus < 0 || row.CurrentStatus > 2 || row.FromStatus < 0 || row.FromStatus > 2 || row.ToStatus < 0 || row.ToStatus > 2 || row.FromStatus == row.ToStatus || row.CreatedAtUnixMs <= 0 || row.CreatedAtUnixMs > maxTaskDueAtUnixMs || row.ReadAtUnixMs < 0 || row.ReadAtUnixMs > maxTaskDueAtUnixMs || (row.ReadAtUnixMs > 0 && row.ReadAtUnixMs < row.CreatedAtUnixMs) {
				return nil, taskDatabaseError(ctx, errors.New("invalid notification row"))
			}
			if _, duplicate := seen[row.NotificationID]; duplicate {
				return nil, taskDatabaseError(ctx, errors.New("duplicate notification row"))
			}
			if row.ReadAtUnixMs == 0 {
				if seenRead || cursor.Phase == "read" || (previousUnread > 0 && row.NotificationID >= previousUnread) {
					return nil, taskDatabaseError(ctx, errors.New("invalid unread notification order"))
				}
				previousUnread = row.NotificationID
			} else {
				seenRead = true
				if previousRead > 0 && row.NotificationID >= previousRead {
					return nil, taskDatabaseError(ctx, errors.New("invalid read notification order"))
				}
				previousRead = row.NotificationID
			}
			seen[row.NotificationID] = struct{}{}
			result.Notifications = append(result.Notifications, &pb.TaskNotificationItem{NotificationId: row.NotificationID, TeamId: row.TeamID, TaskId: row.TaskID, TaskTitle: row.TaskTitle, CurrentStatus: row.CurrentStatus, ActorId: row.ActorID, FromStatus: row.FromStatus, ToStatus: row.ToStatus, CreatedAtUnixMs: row.CreatedAtUnixMs, ReadAtUnixMs: row.ReadAtUnixMs})
		}
		if more && len(rows) > 0 {
			last := rows[len(rows)-1]
			cursor.LastID = last.NotificationID
			if last.ReadAtUnixMs > 0 {
				cursor.Phase = "read"
			}
			result.NextCursor = encodeNotificationCursor(cursor)
		}
	}
	_, currentCaller, currentTeams, err := s.notificationAccess(ctx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	if currentCaller != callerID || !reflect.DeepEqual(currentTeams, teams) {
		return nil, status.Error(codes.Unavailable, "notification access changed")
	}
	return result, nil
}
