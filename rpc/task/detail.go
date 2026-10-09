package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const taskItemColumns = "id, team_id, title, description, creator_id, assignee_id, status, source_group_id, source_message_id, due_at_unix_ms"

type taskItemRow struct {
	DueBucket       int
	ID              int64
	TeamID          int64
	Title           string
	Description     string
	CreatorID       int64
	AssigneeID      *int64
	Status          int8
	SourceGroupID   *int64
	SourceMessageID *int64
	DueAtUnixMs     *int64
}

func (row taskItemRow) item() *pb.TaskItem {
	item := &pb.TaskItem{TaskId: row.ID, TeamId: row.TeamID, Title: row.Title, Description: row.Description, CreatorId: row.CreatorID, Status: int32(row.Status)}
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
	return item
}

func (s *taskServer) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	if s.db == nil || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "task detail is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetTaskId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid task detail parameters")
	}
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, err
	}
	member, err := s.currentTeamMember(teamCtx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	var row taskItemRow
	err = s.db.WithContext(ctx).Table("tasks").Select(taskItemColumns).Where("team_id = ? AND id = ?", req.GetTeamId(), req.GetTaskId()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "task not found")
	}
	if err != nil {
		return nil, taskDatabaseError(ctx, err)
	}
	canUpdate := row.CreatorID == member.GetUserId() || row.AssigneeID != nil && *row.AssigneeID == member.GetUserId() || member.GetRole() == 2
	return &pb.GetTaskResponse{Task: row.item(), CanUpdateStatus: canUpdate}, nil
}
