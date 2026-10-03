package agent

import (
	"context"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftDeadlineUpdater interface {
	updateDraftDeadline(context.Context, taskDraftRun, int64) (taskDraftRun, error)
}

func (s *Server) EditTaskDraftDeadline(ctx context.Context, req *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 || req.GetExpectedRevision() <= 0 || req.DueAtUnixMs == nil || (req.GetDueAtUnixMs() < 0 || req.GetDueAtUnixMs() > maxDraftDueAtUnixMs) {
		return nil, status.Error(codes.InvalidArgument, "invalid deadline editing")
	}
	if s == nil || s.draftReader == nil || s.draftReader.deadline == nil {
		return nil, status.Error(codes.Unavailable, "deadline editing is not configured")
	}
	editCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	run, err := s.draftReader.load(editCtx, token, req.GetRunId())
	if err == nil {
		err = run.requireWaitingConfirmation(run.Scope.InitiatorID)
	}
	if err == nil && run.Revision != req.GetExpectedRevision() {
		err = status.Error(codes.Aborted, "draft changed; reload before editing")
	}
	if err == nil {
		run, err = s.draftReader.deadline.updateDraftDeadline(editCtx, run, req.GetDueAtUnixMs())
	}
	if editCtx.Err() != nil {
		return nil, status.FromContextError(editCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return taskDraftRPCResponse(run), nil
}
