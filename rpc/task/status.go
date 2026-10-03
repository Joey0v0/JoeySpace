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

func (s *taskServer) SetTaskStatus(ctx context.Context, req *pb.SetTaskStatusRequest) (*pb.SetTaskStatusResponse, error) {
	if s.db == nil || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "task status update is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetTaskId() <= 0 || req.GetStatus() < 0 || req.GetStatus() > 2 {
		return nil, status.Error(codes.InvalidArgument, "invalid task status request")
	}
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, err
	}
	member, err := s.currentTeamMember(teamCtx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task struct {
			ID         int64
			CreatorID  int64
			AssigneeID *int64
			Status     int8
		}
		err := tx.Table("tasks").Select("id, creator_id, assignee_id, status").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND team_id = ?", req.GetTaskId(), req.GetTeamId()).Take(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Error(codes.NotFound, "task not found")
		}
		if err != nil {
			return err
		}
		actorID := member.GetUserId()
		if actorID != task.CreatorID && (task.AssigneeID == nil || actorID != *task.AssigneeID) && member.GetRole() != 2 {
			return status.Error(codes.PermissionDenied, "task status update not allowed")
		}
		newStatus := int8(req.GetStatus())
		if task.Status == newStatus {
			return nil
		}
		if err := tx.Table("tasks").Where("id = ? AND team_id = ?", req.GetTaskId(), req.GetTeamId()).Update("status", newStatus).Error; err != nil {
			return err
		}
		operation := struct {
			TaskID     int64
			ActorID    int64
			FromStatus int8
			ToStatus   int8
		}{req.GetTaskId(), actorID, task.Status, newStatus}
		return tx.Table("task_operations").Create(&operation).Error
	})
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, taskDatabaseError(ctx, err)
	}
	return &pb.SetTaskStatusResponse{}, nil
}
