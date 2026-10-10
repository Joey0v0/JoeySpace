package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const skipDraftCollectionItemSQL = `UPDATE agent_task_drafts SET status = ?
    WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'
      AND (task_request_key IS NULL OR task_request_key = '') AND (task_id IS NULL OR task_id = 0)`

func (s *draftStore) skipDraftCollectionItem(ctx context.Context, authorized taskDraftCollection, index int32) (taskDraftCollection, error) {
	if s == nil || s.db == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if !validCollectionConfirmationTarget(authorized, index) {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item identity")
	}
	expected := authorized.Items[index]
	if err := requireCollectionItemSkippable(expected); err != nil {
		return taskDraftCollection{}, err
	}
	var saved taskDraftCollection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := queryDraftCollection(ctx, tx, authorized.ID, authorized.Scope.InitiatorID, true, nil)
		if err != nil {
			return err
		}
		if current.Scope != authorized.Scope || len(current.Items) != len(authorized.Items) || int(index) >= len(current.Items) {
			return status.Error(codes.Aborted, "draft collection changed; reload before skipping")
		}
		run := current.Items[index]
		if err := requireCollectionItemSkippable(run); err != nil {
			return err
		}
		if !sameCollectionItemContent(expected, run) {
			return status.Error(codes.Aborted, "draft item changed; reload before skipping")
		}
		if expected.Status == draftSkipped && run.Status != draftSkipped {
			return status.Error(codes.FailedPrecondition, "skipped item cannot be restored")
		}
		if run.Status == draftWaitingConfirmation {
			result := tx.Exec(skipDraftCollectionItemSQL, string(draftSkipped), current.ID, index, run.Revision)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return status.Error(codes.Aborted, "draft item changed; reload before skipping")
			}
			current.Items[index].Status = draftSkipped
		}
		saved = current
		return nil
	})
	if err != nil {
		return taskDraftCollection{}, draftConfirmationStorageError(ctx, err)
	}
	return saved, nil
}
