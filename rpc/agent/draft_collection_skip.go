package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const draftSkipped draftRunStatus = "skipped"

type draftCollectionItemSkipper interface {
	skipDraftCollectionItem(context.Context, taskDraftCollection, int32) (taskDraftCollection, error)
}

func requireCollectionItemSkippable(run taskDraftRun) error {
	if (run.Status != draftWaitingConfirmation && run.Status != draftSkipped) || run.TaskRequestKey != "" || run.TaskID != 0 {
		return status.Error(codes.FailedPrecondition, "a submitted draft item cannot be skipped")
	}
	return nil
}

func (r *draftAccessReader) skipCollectionItem(ctx context.Context, token string, runID int64, index int32, revision int64) (taskDraftCollection, error) {
	if runID <= 0 || index < 0 || index >= maxGeneratedTaskDrafts || revision <= 0 {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item identity")
	}
	if r == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft skipping is not configured")
	}
	store, ok := r.store.(draftCollectionItemSkipper)
	if !ok {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft skipping is not configured")
	}
	collection, err := r.loadCollection(ctx, token, runID)
	if err != nil {
		return taskDraftCollection{}, err
	}
	if int(index) >= len(collection.Items) {
		return taskDraftCollection{}, status.Error(codes.NotFound, "draft item not found")
	}
	run := collection.Items[index]
	if run.Revision != revision {
		return taskDraftCollection{}, status.Error(codes.Aborted, "draft item changed; reload before skipping")
	}
	if err := requireCollectionItemSkippable(run); err != nil {
		return taskDraftCollection{}, err
	}
	return store.skipDraftCollectionItem(ctx, collection, index)
}
