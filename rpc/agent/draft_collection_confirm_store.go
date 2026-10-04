package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const freezeDraftCollectionItemSQL = `UPDATE agent_task_drafts SET status = ?, task_request_key = ?
    WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'`

const completeDraftCollectionItemSQL = `UPDATE agent_task_drafts SET status = ?, task_id = ?
    WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'creating' AND task_request_key = ?`

type draftCollectionConfirmationStore interface {
	freezeDraftCollectionItem(context.Context, taskDraftCollection, int32, draftCollectionConfirmationReview) (taskDraftCollection, error)
	completeDraftCollectionItem(context.Context, taskDraftCollection, int32, int64) (taskDraftCollection, error)
}

func validCollectionConfirmationTarget(collection taskDraftCollection, index int32) bool {
	return collection.ID > 0 && collection.Scope.TeamID > 0 && collection.Scope.GroupID > 0 && collection.Scope.InitiatorID > 0 &&
		index >= 0 && int(index) < len(collection.Items) && len(collection.Items) <= maxGeneratedTaskDrafts && collection.Items[index].ID == collection.ID &&
		collection.Items[index].Scope == collection.Scope && collection.Items[index].Revision > 0 && validCollectionItemTaskState(collection.Items[index], index)
}

func (s *draftStore) freezeDraftCollectionItem(ctx context.Context, authorized taskDraftCollection, index int32, review draftCollectionConfirmationReview) (taskDraftCollection, error) {
	if s == nil || s.db == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if !validCollectionConfirmationTarget(authorized, index) || !review.valid() {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item confirmation")
	}
	expected := authorized.Items[index]
	if err := review.require(expected); err != nil {
		return taskDraftCollection{}, err
	}
	var saved taskDraftCollection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := queryDraftCollection(ctx, tx, authorized.ID, authorized.Scope.InitiatorID, true, nil)
		if err != nil {
			return err
		}
		if current.Scope != authorized.Scope || len(current.Items) != len(authorized.Items) || int(index) >= len(current.Items) || !validCollectionConfirmationProgress(expected, current.Items[index]) {
			return status.Error(codes.Aborted, "draft item changed; reload before confirming")
		}
		run := current.Items[index]
		if err := review.require(run); err != nil {
			return err
		}
		if run.Status == draftWaitingConfirmation {
			key := draftCollectionTaskRequestKey(current.ID, index)
			result := tx.Exec(freezeDraftCollectionItemSQL, string(draftCreating), key, current.ID, index, run.Revision)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return status.Error(codes.Aborted, "draft item changed; reload before confirming")
			}
			current.Items[index].Status, current.Items[index].TaskRequestKey = draftCreating, key
		}
		saved = current
		return nil
	})
	if err != nil {
		return taskDraftCollection{}, draftConfirmationStorageError(ctx, err)
	}
	return saved, nil
}

func (s *draftStore) completeDraftCollectionItem(ctx context.Context, frozen taskDraftCollection, index int32, taskID int64) (taskDraftCollection, error) {
	if s == nil || s.db == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if !validCollectionConfirmationTarget(frozen, index) || taskID <= 0 || (frozen.Items[index].Status != draftCreating && frozen.Items[index].Status != draftSucceeded) {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item task result")
	}
	expected := frozen.Items[index]
	var saved taskDraftCollection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := queryDraftCollection(ctx, tx, frozen.ID, frozen.Scope.InitiatorID, true, nil)
		if err != nil {
			return err
		}
		if current.Scope != frozen.Scope || len(current.Items) != len(frozen.Items) || int(index) >= len(current.Items) || !validCollectionConfirmationProgress(expected, current.Items[index]) {
			return status.Error(codes.Aborted, "frozen draft item changed; reload before retrying")
		}
		run := current.Items[index]
		if run.Status == draftSucceeded {
			if run.TaskID != taskID {
				return status.Error(codes.AlreadyExists, "task result does not match frozen item")
			}
		} else {
			result := tx.Exec(completeDraftCollectionItemSQL, string(draftSucceeded), taskID, current.ID, index, run.Revision, run.TaskRequestKey)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return status.Error(codes.Aborted, "draft item result changed; reload before retrying")
			}
			current.Items[index].Status, current.Items[index].TaskID = draftSucceeded, taskID
		}
		saved = current
		return nil
	})
	if err != nil {
		return taskDraftCollection{}, draftConfirmationStorageError(ctx, err)
	}
	return saved, nil
}
