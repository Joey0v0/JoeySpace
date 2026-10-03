package agent

import (
	"context"
	"time"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const draftReadTimeout = 12 * time.Second

// ConfigureDraftAccess supplies the User, Agent-owned database, and IM
// dependencies needed for draft reads and text edits.
func (s *Server) ConfigureDraftAccess(db *gorm.DB, users userpb.UserClient, im impb.IMClient) {
	store := &draftStore{db: db}
	s.draftReader = &draftAccessReader{
		identity: &draftIdentityResolver{users: users},
		store:    store,
		editor:   store,
		selector: store,
		deadline: store,
		members:  users,
		im:       im,
	}
}

func (s *Server) GetTaskDraft(ctx context.Context, req *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	if s == nil || s.draftReader == nil {
		return nil, status.Error(codes.Unavailable, "draft access is not configured")
	}
	readCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	run, err := s.draftReader.load(readCtx, token, req.GetRunId())
	if readCtx.Err() != nil {
		return nil, status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return s.draftResponseWithReply(readCtx, run)
}

func (s *Server) EditTaskDraft(ctx context.Context, req *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 || req.GetExpectedRevision() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	if s == nil || s.draftReader == nil {
		return nil, status.Error(codes.Unavailable, "draft editing is not configured")
	}
	editCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	run, err := s.draftReader.editText(editCtx, token, req.GetRunId(),
		req.GetExpectedTitle(), req.GetExpectedDescription(), req.GetTitle(), req.GetDescription(), req.GetExpectedRevision())
	if editCtx.Err() != nil {
		return nil, status.FromContextError(editCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return taskDraftRPCResponse(run), nil
}

func taskDraftRPCResponse(run taskDraftRun) *pb.GetTaskDraftResponse {
	return &pb.GetTaskDraftResponse{
		RunId: run.ID, TeamId: run.Scope.TeamID, GroupId: run.Scope.GroupID,
		Status: string(run.Status),
		TaskId: run.TaskID,
		Draft: &pb.TaskDraftItem{
			Title: run.Draft.Title, Description: run.Draft.Description,
			AssigneeId: run.Draft.AssigneeID, DueAtUnixMs: run.Draft.DueAtUnixMs,
			SourceMessageId: run.Draft.SourceMessageID,
			AssigneeName:    run.Draft.AssigneeName, AssigneeResolution: string(run.Draft.AssigneeResolution),
			Revision: run.Revision,
		},
	}
}
