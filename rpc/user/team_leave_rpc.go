package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// LeaveTeam takes the actor only from the verified login token.
func (s *userServer) LeaveTeam(ctx context.Context, req *pb.LeaveTeamRequest) (*pb.TeamLeaveOperationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "team leave request is required")
	}
	intent, err := s.completeOwnTeamLeave(ctx, req.GetTeamId(), req.GetRequestKey())
	if err != nil {
		return nil, err
	}
	return teamLeaveResponse(intent), nil
}

// GetTeamLeaveOperation remains available after leaving or rejoining: ownership
// comes from the login token and the original, immutable User operation.
func (s *userServer) GetTeamLeaveOperation(ctx context.Context, req *pb.GetTeamLeaveOperationRequest) (*pb.TeamLeaveOperationResponse, error) {
	if err := teamLeaveContextError(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.GetTeamId() <= 0 || !validTeamLeaveRequestKey(req.GetRequestKey()) {
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
		Where("user_id = ? AND request_key = ?", actor.GetId(), req.GetRequestKey()).Take(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "team leave operation not found")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "team leave database unavailable")
	}
	if op.ID <= 0 || op.UserID != actor.GetId() || op.TeamID <= 0 || op.Generation <= 0 || op.RequestKey != req.GetRequestKey() || op.Status < 0 || op.Status > 1 {
		return nil, status.Error(codes.Unavailable, "team leave operation data unavailable")
	}
	if op.TeamID != req.GetTeamId() {
		return nil, status.Error(codes.AlreadyExists, "team leave request belongs to another team")
	}
	return teamLeaveResponse(&teamLeaveIntent{ID: op.ID, TeamID: op.TeamID, Generation: op.Generation, Status: op.Status}), nil
}

func teamLeaveResponse(intent *teamLeaveIntent) *pb.TeamLeaveOperationResponse {
	return &pb.TeamLeaveOperationResponse{
		OperationId: intent.ID,
		TeamId:      intent.TeamID,
		Generation:  intent.Generation,
		Status:      int32(intent.Status),
	}
}
