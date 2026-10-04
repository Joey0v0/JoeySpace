package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (s *Server) ConfirmTaskDraftItem(ctx context.Context, req *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || !validCollectionEditIdentity(req.GetRunId(), req.ItemIndex, req.GetExpectedRevision()) || req.ExpectedAssigneeId == nil || req.ExpectedDueAtUnixMs == nil {
		return nil, status.Error(codes.InvalidArgument, "draft item identity and review values are required")
	}
	review := draftCollectionConfirmationReview{Revision: req.GetExpectedRevision(), Title: req.GetExpectedTitle(), Description: req.GetExpectedDescription(), AssigneeID: req.GetExpectedAssigneeId(), DueAtUnixMs: req.GetExpectedDueAtUnixMs(), DeadlineResolution: req.GetExpectedDeadlineResolution()}
	if !review.valid() {
		return nil, status.Error(codes.InvalidArgument, "invalid draft item confirmation")
	}
	if s == nil || s.draftReader == nil || s.confirmer == nil || s.confirmer.store == nil || s.confirmer.tasks == nil {
		return nil, status.Error(codes.Unavailable, "draft item confirmation is not configured")
	}
	if _, ok := s.confirmer.store.(draftCollectionConfirmationStore); !ok {
		return nil, status.Error(codes.Unavailable, "draft collection confirmation is not configured")
	}
	confirmCtx, cancel := context.WithTimeout(ctx, draftConfirmTimeout)
	defer cancel()
	createCtx, stopCreate := context.WithTimeout(confirmCtx, draftCreatePhaseTimeout)
	defer stopCreate()
	collection, err := s.draftReader.loadCollection(createCtx, token, req.GetRunId())
	index := req.GetItemIndex()
	if err == nil && int(index) >= len(collection.Items) {
		err = status.Error(codes.NotFound, "draft item not found")
	}
	if err == nil && collection.Items[index].Status == draftSkipped {
		err = status.Error(codes.FailedPrecondition, "skipped draft item cannot be confirmed")
	}
	if err == nil {
		err = review.require(collection.Items[index])
	}
	if err == nil && collection.Items[index].Status == draftWaitingConfirmation {
		err = s.draftReader.checkAssignee(createCtx, token, collection.Scope.TeamID, collection.Items[index].Draft.AssigneeID)
	}
	if err == nil {
		collection, err = s.confirmer.confirmCollectionItem(createCtx, token, collection, index, review)
	}
	if confirmCtx.Err() != nil {
		return nil, status.FromContextError(confirmCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	// Collection items never invoke the old run-level reply implementation.
	response := s.taskDraftCollectionResponse(collection)
	return &pb.GetTaskDraftItemResponse{RunId: response.RunId, TeamId: response.TeamId, GroupId: response.GroupId, ItemCount: response.ItemCount, Item: response.Items[index]}, nil
}

func (c *draftConfirmer) confirmCollectionItem(ctx context.Context, token string, authorized taskDraftCollection, index int32, review draftCollectionConfirmationReview) (taskDraftCollection, error) {
	if c == nil || c.tasks == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft confirmation is not configured")
	}
	store, ok := c.store.(draftCollectionConfirmationStore)
	if !ok {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft collection confirmation is not configured")
	}
	if !validCollectionConfirmationTarget(authorized, index) || !review.valid() {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item confirmation")
	}
	if authorized.Items[index].Status == draftSkipped {
		return taskDraftCollection{}, status.Error(codes.FailedPrecondition, "skipped draft item cannot be confirmed")
	}
	if err := review.require(authorized.Items[index]); err != nil {
		return taskDraftCollection{}, err
	}
	frozen, err := store.freezeDraftCollectionItem(ctx, authorized, index, review)
	if err != nil {
		return taskDraftCollection{}, err
	}
	if !validCollectionConfirmationTarget(frozen, index) || frozen.ID != authorized.ID || frozen.Scope != authorized.Scope || len(frozen.Items) != len(authorized.Items) || !validCollectionConfirmationProgress(authorized.Items[index], frozen.Items[index]) {
		return taskDraftCollection{}, status.Error(codes.Aborted, "draft item changed while confirming")
	}
	run := frozen.Items[index]
	if err := review.require(run); err != nil {
		return taskDraftCollection{}, err
	}
	if run.Status == draftSucceeded {
		return frozen, nil
	}
	if run.Status != draftCreating {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft item was not frozen")
	}
	taskCtx, cancel := context.WithTimeout(ctx, draftTaskTimeout)
	defer cancel()
	taskCtx = metadata.NewOutgoingContext(taskCtx, metadata.Pairs("authorization", "Bearer "+token, "idempotency-key", run.TaskRequestKey))
	var sourceGroupID int64
	if run.Draft.SourceMessageID > 0 {
		sourceGroupID = run.Scope.GroupID
	}
	result, err := c.tasks.CreateTask(taskCtx, &taskpb.CreateTaskRequest{TeamId: run.Scope.TeamID, Title: run.Draft.Title, Description: run.Draft.Description, AssigneeId: run.Draft.AssigneeID, DueAtUnixMs: run.Draft.DueAtUnixMs, SourceGroupId: sourceGroupID, SourceMessageId: run.Draft.SourceMessageID})
	if taskCtx.Err() != nil {
		return taskDraftCollection{}, status.FromContextError(taskCtx.Err()).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.InvalidArgument, codes.FailedPrecondition, codes.AlreadyExists:
			return taskDraftCollection{}, status.Error(status.Code(err), "task creation rejected; reload the item to check its state")
		case codes.DeadlineExceeded, codes.Canceled:
			return taskDraftCollection{}, status.Error(status.Code(err), "task creation result pending; retry the same item")
		default:
			return taskDraftCollection{}, status.Error(codes.Unavailable, "task creation unavailable; result may be pending")
		}
	}
	if result == nil || result.GetTaskId() <= 0 {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "task creation returned no result; retry the same item")
	}
	saved, err := store.completeDraftCollectionItem(ctx, frozen, index, result.GetTaskId())
	if err != nil {
		return taskDraftCollection{}, err
	}
	if !validCollectionConfirmationTarget(saved, index) || saved.ID != frozen.ID || saved.Scope != frozen.Scope || len(saved.Items) != len(frozen.Items) ||
		!sameCollectionItemContent(saved.Items[index], run) || saved.Items[index].TaskRequestKey != run.TaskRequestKey || saved.Items[index].Status != draftSucceeded || saved.Items[index].TaskID != result.GetTaskId() {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "task result was not saved for the frozen item")
	}
	return saved, nil
}
