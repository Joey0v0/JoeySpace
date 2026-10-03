package agent

import (
	"context"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftCollectionLoader interface {
	loadDraftCollectionForInitiator(context.Context, int64, int64) (taskDraftCollection, error)
}

func (r *draftAccessReader) loadCollection(ctx context.Context, token string, runID int64) (taskDraftCollection, error) {
	if r == nil || r.identity == nil || r.im == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft access is not configured")
	}
	store, ok := r.store.(draftCollectionLoader)
	if !ok {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft collection access is not configured")
	}
	if runID <= 0 {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	actorID, err := r.identity.currentUserID(ctx, token)
	if err != nil {
		return taskDraftCollection{}, err
	}
	collection, err := store.loadDraftCollectionForInitiator(ctx, runID, actorID)
	if err != nil {
		return taskDraftCollection{}, err
	}
	if collection.ID != runID || collection.Scope.InitiatorID != actorID || collection.Scope.TeamID <= 0 || collection.Scope.GroupID <= 0 {
		return taskDraftCollection{}, status.Error(codes.NotFound, "draft run not found")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return taskDraftCollection{}, err
	}
	defer cancel()
	_, err = r.im.CheckTeamGroupAccess(readCtx, &impb.CheckTeamGroupAccessRequest{TeamId: collection.Scope.TeamID, GroupId: collection.Scope.GroupID})
	if readCtx.Err() != nil {
		return taskDraftCollection{}, status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
			return taskDraftCollection{}, err
		default:
			return taskDraftCollection{}, status.Error(codes.Unavailable, "team group access check unavailable")
		}
	}
	return collection, nil
}
