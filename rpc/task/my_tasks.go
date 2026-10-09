package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *taskServer) ListMyTasks(ctx context.Context, req *pb.ListMyTasksRequest) (*pb.ListMyTasksResponse, error) {
	if s.db == nil || s.teamClient == nil || s.identityClient == nil || s.teamDirectory == nil {
		return nil, status.Error(codes.Unavailable, "personal task listing is not enabled")
	}
	if req == nil || req.GetView() < 0 || req.GetView() > 1 || req.GetTeamId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 50 || len(req.GetCursor()) > 2048 {
		return nil, status.Error(codes.InvalidArgument, "invalid personal task parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	cursor := personalTaskCursor{Version: 1, View: req.GetView(), TeamID: req.GetTeamId()}
	if req.GetCursor() != "" {
		var err error
		cursor, err = decodePersonalTaskCursor(req.GetCursor(), req.GetView(), req.GetTeamId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid task cursor")
		}
	}
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, err
	}
	caller, err := s.identityClient.GetMyInfo(teamCtx, &userpb.GetMyInfoRequest{})
	if err != nil {
		return nil, taskTeamError(err)
	}
	if caller.GetId() <= 0 {
		return nil, status.Error(codes.Unavailable, "caller identity response invalid")
	}
	teams, err := s.personalTaskTeams(teamCtx, req.GetTeamId(), caller.GetId())
	if err != nil {
		return nil, err
	}
	result := &pb.ListMyTasksResponse{Tasks: make([]*pb.TaskItem, 0)}
	if len(teams) == 0 {
		return result, nil
	}
	if req.GetCursor() == "" {
		now := time.Now
		if s.now != nil {
			now = s.now
		}
		cursor.SnapshotNowUnixMs = now().UTC().UnixMilli()
		var upper struct{ UpperTaskID sql.NullInt64 }
		err := s.db.WithContext(ctx).Table("tasks").Select("MAX(id) AS upper_task_id").Where("assignee_id = ? AND team_id IN ?", caller.GetId(), teams).Scan(&upper).Error
		if err != nil {
			return nil, taskDatabaseError(ctx, err)
		}
		if !upper.UpperTaskID.Valid || upper.UpperTaskID.Int64 <= 0 {
			return result, nil
		}
		cursor.SnapshotUpperTaskID = upper.UpperTaskID.Int64
	}
	query := s.db.WithContext(ctx).Table("tasks").Where("assignee_id = ? AND team_id IN ? AND id <= ?", caller.GetId(), teams, cursor.SnapshotUpperTaskID)
	if req.GetView() == 1 {
		query = query.Select(taskItemColumns).Where("status = ?", 2)
		if req.GetCursor() != "" {
			query = query.Where("id < ?", cursor.LastTaskID)
		}
		query = query.Order("id DESC")
	} else {
		bucket := fmt.Sprintf("CASE WHEN due_at_unix_ms IS NULL THEN 3 WHEN due_at_unix_ms < %d THEN 0 WHEN due_at_unix_ms <= %d THEN 1 ELSE 2 END", cursor.SnapshotNowUnixMs, cursor.SnapshotNowUnixMs+taskDueWeekMs)
		query = query.Select(taskItemColumns+", "+bucket+" AS due_bucket").Where("status IN ?", []int{0, 1})
		if req.GetCursor() != "" {
			if cursor.LastBucket == 3 {
				query = query.Where("("+bucket+" = ? AND id > ?)", 3, cursor.LastTaskID)
			} else {
				query = query.Where("("+bucket+" > ? OR ("+bucket+" = ? AND (due_at_unix_ms > ? OR (due_at_unix_ms = ? AND id > ?))))", cursor.LastBucket, cursor.LastBucket, cursor.LastDueAtUnixMs, cursor.LastDueAtUnixMs, cursor.LastTaskID)
			}
		}
		query = query.Order("due_bucket ASC").Order("due_at_unix_ms ASC").Order("id ASC")
	}
	var rows []taskItemRow
	if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, taskDatabaseError(ctx, err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		cursor.LastTaskID, cursor.LastStatus = last.ID, int32(last.Status)
		if req.GetView() == 0 {
			cursor.LastBucket = last.DueBucket
			cursor.LastDueAtUnixMs = 0
			if last.DueAtUnixMs != nil {
				cursor.LastDueAtUnixMs = *last.DueAtUnixMs
			}
		}
		result.NextCursor = encodePersonalTaskCursor(cursor)
	}
	for _, row := range rows {
		result.Tasks = append(result.Tasks, row.item())
	}
	return result, nil
}

func (s *taskServer) personalTaskTeams(teamCtx context.Context, teamID, callerID int64) ([]int64, error) {
	if teamID > 0 {
		member, err := s.currentTeamMember(teamCtx, teamID)
		if err != nil {
			return nil, err
		}
		if member.GetUserId() != callerID {
			return nil, status.Error(codes.Unavailable, "team member identity invalid")
		}
		return []int64{teamID}, nil
	}
	var teams []int64
	var after int64
	for {
		page, err := s.teamDirectory.ListMyTeams(teamCtx, &userpb.ListMyTeamsRequest{AfterTeamId: after, Limit: 100})
		if err != nil {
			return nil, taskTeamError(err)
		}
		invalid := status.Error(codes.Unavailable, "team directory response invalid")
		if page == nil {
			return nil, invalid
		}
		last := after
		for _, team := range page.GetTeams() {
			if team == nil || team.GetTeamId() <= last {
				return nil, invalid
			}
			last = team.GetTeamId()
			teams = append(teams, last)
		}
		next := page.GetNextAfterTeamId()
		if next == 0 {
			return teams, nil
		}
		if next <= after || len(page.GetTeams()) == 0 || next != last {
			return nil, invalid
		}
		after = next
	}
}
