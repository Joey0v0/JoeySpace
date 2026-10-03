package agent

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/yjydist/go-im/rpc/agent/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const (
	draftConfirmTimeout     = 18 * time.Second
	draftCreatePhaseTimeout = 12 * time.Second
	draftTaskTimeout        = 5 * time.Second
)

type draftConfirmationStore interface {
	freezeDraft(context.Context, int64, int64, string, string, int64, *int64, *int64, string) (taskDraftRun, error)
	completeDraft(context.Context, int64, int64, string, int64) (taskDraftRun, error)
}

type draftTaskCreator interface {
	CreateTask(context.Context, *taskpb.CreateTaskRequest, ...grpc.CallOption) (*taskpb.CreateTaskResponse, error)
}

type draftConfirmer struct {
	store draftConfirmationStore
	tasks draftTaskCreator
}

// ConfigureDraftConfirmation uses Agent-owned state and the existing Task RPC.
// ConfigureDraftAccess must also supply current user/group checks.
func (s *Server) ConfigureDraftConfirmation(db *gorm.DB, tasks taskpb.TaskClient) {
	s.confirmer = &draftConfirmer{store: &draftStore{db: db}, tasks: tasks}
}

func (s *Server) ConfirmTaskDraft(ctx context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 || req.GetExpectedRevision() <= 0 || !utf8.ValidString(req.GetExpectedTitle()) ||
		utf8.RuneCountInString(req.GetExpectedTitle()) < 1 || utf8.RuneCountInString(req.GetExpectedTitle()) > 200 ||
		!utf8.ValidString(req.GetExpectedDescription()) || utf8.RuneCountInString(req.GetExpectedDescription()) > 2000 ||
		(req.ExpectedAssigneeId != nil && req.GetExpectedAssigneeId() < 0) ||
		(req.ExpectedDueAtUnixMs != nil && (req.GetExpectedDueAtUnixMs() < 0 || req.GetExpectedDueAtUnixMs() > maxDraftDueAtUnixMs)) || !validDeadlineResolution(req.GetExpectedDeadlineResolution()) {
		return nil, status.Error(codes.InvalidArgument, "invalid draft confirmation")
	}
	if s == nil || s.draftReader == nil || s.confirmer == nil || s.confirmer.store == nil || s.confirmer.tasks == nil {
		return nil, status.Error(codes.Unavailable, "draft confirmation is not configured")
	}
	confirmCtx, cancel := context.WithTimeout(ctx, draftConfirmTimeout)
	defer cancel()
	createCtx, stopCreate := context.WithTimeout(confirmCtx, draftCreatePhaseTimeout)
	defer stopCreate()
	run, err := s.draftReader.load(createCtx, token, req.GetRunId())
	alreadySucceeded := run.Status == draftSucceeded
	if err == nil {
		err = run.Draft.requireAssigneeReview(req.ExpectedAssigneeId)
	}
	if err == nil && (run.Revision != req.GetExpectedRevision() || run.Draft.Title != req.GetExpectedTitle() || run.Draft.Description != req.GetExpectedDescription()) {
		err = status.Error(codes.Aborted, "draft changed; reload before confirming")
	}
	if err == nil {
		err = run.Draft.requireDeadlineStateReview(req.ExpectedDueAtUnixMs, req.GetExpectedDeadlineResolution())
	}
	if err == nil && run.Status == draftWaitingConfirmation {
		err = s.draftReader.checkAssignee(createCtx, token, run.Scope.TeamID, run.Draft.AssigneeID)
	}
	if err == nil {
		run, err = s.confirmer.confirm(createCtx, token, run, req.GetExpectedTitle(), req.GetExpectedDescription(), req.GetExpectedRevision(), req.ExpectedAssigneeId, req.ExpectedDueAtUnixMs, req.GetExpectedDeadlineResolution())
	}
	if confirmCtx.Err() != nil {
		return nil, status.FromContextError(confirmCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	stopCreate()
	if s.replier == nil || alreadySucceeded {
		return s.draftResponseWithReply(confirmCtx, run)
	}
	// A saved task remains successful even if posting or its acknowledgement fails.
	// Replayed confirmations do not silently retry; RetryTaskReply is explicit.
	record, replyErr := s.replier.attempt(confirmCtx, token, run)
	response := taskDraftRPCResponse(run)
	if record.RunID > 0 {
		applyDraftReply(response, record, true)
	} else if replyErr != nil {
		response.ReplyStatus = "unknown"
	}
	return response, nil
}

func (c *draftConfirmer) confirm(ctx context.Context, token string, authorized taskDraftRun, expectedTitle, expectedDescription string, expectedRevision int64, expectedAssigneeID, expectedDueAtUnixMs *int64, expectedDeadlineResolution string) (taskDraftRun, error) {
	if authorized.Revision != expectedRevision {
		return taskDraftRun{}, status.Error(codes.Aborted, "draft changed; reload before confirming")
	}
	if err := authorized.Draft.requireAssigneeReview(expectedAssigneeID); err != nil {
		return taskDraftRun{}, err
	}
	if err := authorized.Draft.requireDeadlineStateReview(expectedDueAtUnixMs, expectedDeadlineResolution); err != nil {
		return taskDraftRun{}, err
	}
	run, err := c.store.freezeDraft(ctx, authorized.ID, authorized.Scope.InitiatorID, expectedTitle, expectedDescription, expectedRevision, expectedAssigneeID, expectedDueAtUnixMs, expectedDeadlineResolution)
	if err != nil {
		return taskDraftRun{}, err
	}
	if run.ID != authorized.ID || run.Scope != authorized.Scope || run.Revision != expectedRevision || run.Draft != authorized.Draft {
		return taskDraftRun{}, status.Error(codes.Aborted, "draft scope changed; reload before confirming")
	}
	if err := run.Draft.requireAssigneeReview(expectedAssigneeID); err != nil {
		return taskDraftRun{}, err
	}
	if err := run.Draft.requireDeadlineStateReview(expectedDueAtUnixMs, expectedDeadlineResolution); err != nil {
		return taskDraftRun{}, err
	}
	if run.Status == draftSucceeded {
		return run, nil
	}
	// The store has committed the frozen intent before any Task network call.
	taskCtx, cancel := context.WithTimeout(ctx, draftTaskTimeout)
	defer cancel()
	taskCtx = metadata.NewOutgoingContext(taskCtx, metadata.Pairs(
		"authorization", "Bearer "+token, "idempotency-key", run.TaskRequestKey))
	var sourceGroupID int64
	if run.Draft.SourceMessageID > 0 {
		sourceGroupID = run.Scope.GroupID
	}
	result, err := c.tasks.CreateTask(taskCtx, &taskpb.CreateTaskRequest{
		TeamId: run.Scope.TeamID, Title: run.Draft.Title, Description: run.Draft.Description,
		AssigneeId: run.Draft.AssigneeID, DueAtUnixMs: run.Draft.DueAtUnixMs,
		SourceGroupId: sourceGroupID, SourceMessageId: run.Draft.SourceMessageID,
	})
	if taskCtx.Err() != nil {
		return taskDraftRun{}, status.FromContextError(taskCtx.Err()).Err()
	}
	if err != nil {
		// Never unfreeze: this or an earlier attempt might have committed in Task.
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.InvalidArgument,
			codes.FailedPrecondition, codes.AlreadyExists:
			return taskDraftRun{}, status.Error(status.Code(err), "task creation rejected; reload draft to check its state")
		case codes.DeadlineExceeded, codes.Canceled:
			return taskDraftRun{}, status.Error(status.Code(err), "task creation result pending; retry the same run")
		default:
			return taskDraftRun{}, status.Error(codes.Unavailable, "task creation unavailable; result may be pending")
		}
	}
	if result == nil || result.GetTaskId() <= 0 {
		return taskDraftRun{}, status.Error(codes.Unavailable, "task creation returned no result; retry the same run")
	}
	return c.store.completeDraft(ctx, run.ID, run.Scope.InitiatorID, run.TaskRequestKey, result.GetTaskId())
}
