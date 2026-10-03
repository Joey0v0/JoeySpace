package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftCollectionEditKind string

const (
	collectionEditText     draftCollectionEditKind = "text"
	collectionEditAssignee draftCollectionEditKind = "assignee"
	collectionEditDeadline draftCollectionEditKind = "deadline"
)

type draftCollectionEdit struct {
	Kind        draftCollectionEditKind
	Title       string
	Description string
	AssigneeID  int64
	DueAtUnixMs int64
}

type draftCollectionItemUpdater interface {
	updateDraftCollectionItem(context.Context, taskDraftCollection, int32, taskDraft, draftCollectionEditKind) (taskDraftCollection, error)
}

func (r *draftAccessReader) editCollectionItem(ctx context.Context, token string, runID int64, index int32, revision int64, edit draftCollectionEdit) (taskDraftCollection, error) {
	if runID <= 0 || index < 0 || index >= maxGeneratedTaskDrafts || revision <= 0 {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item identity")
	}
	if r == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft collection editing is not configured")
	}
	updater, ok := r.store.(draftCollectionItemUpdater)
	if !ok {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft collection editing is not configured")
	}
	collection, err := r.loadCollectionWithEditTarget(ctx, token, runID, &index)
	if err != nil {
		return taskDraftCollection{}, err
	}
	if int(index) >= len(collection.Items) {
		return taskDraftCollection{}, status.Error(codes.NotFound, "draft item not found")
	}
	run := collection.Items[index]
	if err := run.requireWaitingConfirmation(collection.Scope.InitiatorID); err != nil {
		return taskDraftCollection{}, err
	}
	if run.TaskRequestKey != "" || run.TaskID != 0 {
		return taskDraftCollection{}, status.Error(codes.FailedPrecondition, "draft item has a task request")
	}
	if run.Revision != revision {
		return taskDraftCollection{}, status.Error(codes.Aborted, "draft item changed; reload before editing")
	}
	candidate := run.Draft
	switch edit.Kind {
	case collectionEditText:
		candidate.Title, candidate.Description = edit.Title, edit.Description
	case collectionEditAssignee:
		if edit.AssigneeID < 0 {
			return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid assignee selection")
		}
		if err := r.checkAssignee(ctx, token, collection.Scope.TeamID, edit.AssigneeID); err != nil {
			return taskDraftCollection{}, err
		}
		candidate.AssigneeID, candidate.AssigneeResolution = edit.AssigneeID, assigneeUnassigned
		if edit.AssigneeID > 0 {
			candidate.AssigneeResolution = assigneeSelected
		}
	case collectionEditDeadline:
		if edit.DueAtUnixMs < 0 || edit.DueAtUnixMs > maxDraftDueAtUnixMs {
			return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft deadline")
		}
		candidate.DueAtUnixMs, candidate.Deadline.Resolution = edit.DueAtUnixMs, "unset"
		if edit.DueAtUnixMs > 0 {
			candidate.Deadline.Resolution = "selected"
		}
	default:
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item edit")
	}
	validated, err := newWaitingTaskDraftRun(collection.Scope, candidate)
	if err != nil {
		return taskDraftCollection{}, err
	}
	return updater.updateDraftCollectionItem(ctx, collection, index, validated.Draft, edit.Kind)
}
