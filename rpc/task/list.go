package main

import (
	"context"

	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *taskServer) ListTeamTasks(ctx context.Context, req *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error) {
	if s.db == nil || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "task listing is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetAfterTaskId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid task list parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.currentTeamMember(teamCtx, req.GetTeamId()); err != nil {
		return nil, err
	}
	var rows []struct {
		ID              int64
		Title           string
		Description     string
		CreatorID       int64
		AssigneeID      *int64
		SourceGroupID   *int64
		SourceMessageID *int64
		DueAtUnixMs     *int64
		Status          int8
	}
	err = s.db.WithContext(ctx).Table("tasks").Select("id, title, description, creator_id, assignee_id, status, source_group_id, source_message_id, due_at_unix_ms").
		Where("team_id = ? AND id > ?", req.GetTeamId(), req.GetAfterTaskId()).
		Order("id ASC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return nil, taskDatabaseError(ctx, err)
	}
	result := &pb.ListTeamTasksResponse{Tasks: make([]*pb.TaskItem, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextAfterTaskId = rows[len(rows)-1].ID
	}
	for _, row := range rows {
		item := &pb.TaskItem{TaskId: row.ID, Title: row.Title, Description: row.Description, CreatorId: row.CreatorID, Status: int32(row.Status)}
		if row.AssigneeID != nil {
			item.AssigneeId = *row.AssigneeID
		}
		if row.SourceGroupID != nil {
			item.SourceGroupId = *row.SourceGroupID
		}
		if row.SourceMessageID != nil {
			item.SourceMessageId = *row.SourceMessageID
		}
		if row.DueAtUnixMs != nil {
			item.DueAtUnixMs = *row.DueAtUnixMs
		}
		result.Tasks = append(result.Tasks, item)
	}
	return result, nil
}
