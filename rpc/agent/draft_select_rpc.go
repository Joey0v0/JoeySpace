package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftMemberClient interface {
	CheckTeamMemberByID(context.Context, *userpb.CheckTeamMemberByIDRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberByIDResponse, error)
}

type draftAssigneeUpdater interface {
	updateDraftAssignee(context.Context, taskDraftRun, int64) (taskDraftRun, error)
}

func (r *draftAccessReader) checkAssignee(ctx context.Context, token string, teamID, assigneeID int64) error {
	if assigneeID == 0 {
		return nil
	}
	if r == nil || r.members == nil {
		return status.Error(codes.Unavailable, "assignee membership check is not configured")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := r.members.CheckTeamMemberByID(readCtx, &userpb.CheckTeamMemberByIDRequest{TeamId: teamID, UserId: assigneeID})
	if readCtx.Err() != nil {
		return status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
			return status.Error(status.Code(err), "assignee membership check rejected")
		default:
			return status.Error(codes.Unavailable, "assignee membership check unavailable")
		}
	}
	if response == nil {
		return status.Error(codes.Unavailable, "invalid assignee membership response")
	}
	return nil
}

func (s *Server) SelectTaskDraftAssignee(ctx context.Context, req *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 || req.GetExpectedRevision() <= 0 || req.AssigneeId == nil || req.GetAssigneeId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid assignee selection")
	}
	if s == nil || s.draftReader == nil || s.draftReader.selector == nil {
		return nil, status.Error(codes.Unavailable, "assignee selection is not configured")
	}
	selectCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	run, err := s.draftReader.load(selectCtx, token, req.GetRunId())
	if err == nil {
		err = run.requireWaitingConfirmation(run.Scope.InitiatorID)
	}
	if err == nil && run.Revision != req.GetExpectedRevision() {
		err = status.Error(codes.Aborted, "draft changed; reload before choosing")
	}
	if err == nil {
		err = s.draftReader.checkAssignee(selectCtx, token, run.Scope.TeamID, req.GetAssigneeId())
	}
	if err == nil {
		run, err = s.draftReader.selector.updateDraftAssignee(selectCtx, run, req.GetAssigneeId())
	}
	if selectCtx.Err() != nil {
		return nil, status.FromContextError(selectCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return taskDraftRPCResponse(run), nil
}
