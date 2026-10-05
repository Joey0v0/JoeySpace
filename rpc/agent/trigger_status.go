package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type taskTriggerStatusSource interface {
	Read(context.Context, int64) (*impb.ReadTaskTriggerContextResponse, error)
}

type taskTriggerStatusLoader interface {
	loadTaskTriggerStatus(context.Context, int64) (taskTriggerStatus, error)
}

type taskTriggerStatusReader struct {
	source taskTriggerStatusSource
	store  taskTriggerStatusLoader
}

// The source is the worker's existing dedicated mTLS client; no new client or
// user credential is constructed. Disabled background configuration stays off.
func (s *Server) ConfigureTaskTriggerStatus(source *TriggerContextClient, store *TriggerInboxStore) {
	if s == nil {
		return
	}
	if source == nil || source.rpc == nil || store == nil || store.db == nil {
		s.triggerStatus = nil
		return
	}
	s.triggerStatus = &taskTriggerStatusReader{source: source, store: store}
}

func (s *Server) GetTaskTriggerStatus(ctx context.Context, req *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.InvalidArgument, "trigger status context required")
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	messageID := req.GetMessageId()
	if messageID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive trigger message ID required")
	}
	if s == nil || s.draftReader == nil || s.draftReader.identity == nil || s.triggerStatus == nil || s.triggerStatus.source == nil || s.triggerStatus.store == nil {
		return nil, status.Error(codes.Unavailable, "trigger status access is not configured")
	}
	readCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	actorID, err := s.draftReader.identity.currentUserID(readCtx, token)
	if readCtx.Err() != nil || err != nil {
		return nil, taskTriggerStatusError(readCtx, err)
	}
	// Read takes only a saved message ID, and strips caller metadata itself.
	source, err := s.triggerStatus.source.Read(readCtx, messageID)
	if readCtx.Err() != nil || err != nil {
		return nil, taskTriggerStatusError(readCtx, err)
	}
	if !validTriggerContext(source, messageID) {
		return nil, status.Error(codes.Unavailable, "invalid persisted trigger context")
	}
	if source.ActorId != actorID {
		return nil, status.Error(codes.NotFound, "task trigger not found")
	}
	scope := draftRunScope{TeamID: source.TeamId, GroupID: source.GroupId, InitiatorID: actorID}
	saved, err := s.triggerStatus.store.loadTaskTriggerStatus(readCtx, messageID)
	if readCtx.Err() != nil || err != nil {
		return nil, taskTriggerStatusError(readCtx, err)
	}
	if !saved.valid(messageID) {
		return nil, status.Error(codes.Unavailable, "invalid stored trigger status")
	}
	if saved.Status == TriggerInboxCompleted {
		collection, err := s.draftReader.loadCollection(readCtx, token, saved.RunID)
		if readCtx.Err() != nil || err != nil {
			return nil, taskTriggerStatusError(readCtx, err)
		}
		if collection.ID != saved.RunID || collection.Scope != scope {
			return nil, status.Error(codes.Unavailable, "trigger draft association is invalid")
		}
	}
	if readCtx.Err() != nil {
		return nil, status.FromContextError(readCtx.Err()).Err()
	}
	return &pb.GetTaskTriggerStatusResponse{MessageId: messageID, TeamId: scope.TeamID, GroupId: scope.GroupID, Status: saved.Status, RunId: saved.RunID}, nil
}

func taskTriggerStatusError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	code := status.Code(err)
	switch code {
	case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.Canceled, codes.DeadlineExceeded:
	default:
		code = codes.Unavailable
	}
	return status.Error(code, "trigger status access unavailable")
}
