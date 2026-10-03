package agent

import (
	"context"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftRunLoader interface {
	loadDraftForInitiator(context.Context, int64, int64) (taskDraftRun, error)
}

type draftTextUpdater interface {
	updateDraftText(context.Context, taskDraftRun, string, string) error
}

type draftGroupAccessClient interface {
	CheckTeamGroupAccess(context.Context, *impb.CheckTeamGroupAccessRequest, ...grpc.CallOption) (*impb.CheckTeamGroupAccessResponse, error)
}

type draftAccessReader struct {
	identity *draftIdentityResolver
	store    draftRunLoader
	editor   draftTextUpdater
	im       draftGroupAccessClient
}

// load checks the current Token, draft ownership, and current team/group
// membership before returning a draft.
func (r *draftAccessReader) load(ctx context.Context, token string, runID int64) (taskDraftRun, error) {
	if r == nil || r.identity == nil || r.store == nil || r.im == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft access is not configured")
	}
	if runID <= 0 {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	actorID, err := r.identity.currentUserID(ctx, token)
	if err != nil {
		return taskDraftRun{}, err
	}
	run, err := r.store.loadDraftForInitiator(ctx, runID, actorID)
	if err != nil {
		return taskDraftRun{}, err
	}
	if run.ID != runID || run.Scope.InitiatorID != actorID || run.Scope.TeamID <= 0 || run.Scope.GroupID <= 0 {
		return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return taskDraftRun{}, err
	}
	defer cancel()
	_, err = r.im.CheckTeamGroupAccess(readCtx, &impb.CheckTeamGroupAccessRequest{
		TeamId: run.Scope.TeamID, GroupId: run.Scope.GroupID,
	})
	if readCtx.Err() != nil {
		return taskDraftRun{}, status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
			return taskDraftRun{}, err
		default:
			return taskDraftRun{}, status.Error(codes.Unavailable, "team group access check unavailable")
		}
	}
	return run, nil
}

// editText reuses the current ownership and team/group checks before changing
// only the human-readable fields of a waiting draft.
func (r *draftAccessReader) editText(ctx context.Context, token string, runID int64, expectedTitle, expectedDescription, title, description string, expectedRevision int64) (taskDraftRun, error) {
	if r == nil || r.editor == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft editing is not configured")
	}
	run, err := r.load(ctx, token, runID)
	if err != nil {
		return taskDraftRun{}, err
	}
	if err := run.requireWaitingConfirmation(run.Scope.InitiatorID); err != nil {
		return taskDraftRun{}, err
	}
	if expectedRevision <= 0 {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "draft revision is required")
	}
	if run.Revision != expectedRevision || run.Draft.Title != expectedTitle || run.Draft.Description != expectedDescription {
		return taskDraftRun{}, status.Error(codes.Aborted, "draft changed; reload before editing")
	}
	candidate := run.Draft
	candidate.Title, candidate.Description = title, description
	validated, err := newWaitingTaskDraftRun(run.Scope, candidate)
	if err != nil {
		return taskDraftRun{}, err
	}
	changed := run.Draft != validated.Draft
	if changed && run.Revision == int64(1<<63-1) {
		return taskDraftRun{}, status.Error(codes.FailedPrecondition, "draft revision exhausted")
	}
	if err := r.editor.updateDraftText(ctx, run, validated.Draft.Title, validated.Draft.Description); err != nil {
		return taskDraftRun{}, err
	}
	if changed {
		run.Revision++
	}
	run.Draft = validated.Draft
	return run, nil
}
