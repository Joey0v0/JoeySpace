package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// Current ownership and group access were checked before the transaction.
// The complete locked draft and revision protect a simultaneous edit or freeze.
func (s *draftStore) updateDraftDeadline(ctx context.Context, authorized taskDraftRun, dueAtUnixMs int64) (taskDraftRun, error) {
	if s == nil || s.db == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if authorized.ID <= 0 || authorized.Scope.InitiatorID <= 0 || authorized.Revision <= 0 || (dueAtUnixMs < 0 || dueAtUnixMs > maxDraftDueAtUnixMs) {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid deadline edit")
	}
	var run taskDraftRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		run, err = lockedDraft(tx, authorized.ID, authorized.Scope.InitiatorID)
		if err != nil {
			return err
		}
		if err = run.requireWaitingConfirmation(authorized.Scope.InitiatorID); err != nil {
			return err
		}
		if run.Scope != authorized.Scope || run.Revision != authorized.Revision || run.Draft != authorized.Draft {
			return status.Error(codes.Aborted, "draft changed; reload before editing")
		}
		current, err := newWaitingTaskDraftRun(run.Scope, run.Draft)
		if err != nil || current.Draft != run.Draft {
			return status.Error(codes.Unavailable, "stored task draft is invalid")
		}
		candidate := run.Draft
		candidate.DueAtUnixMs = dueAtUnixMs
		validated, err := newWaitingTaskDraftRun(run.Scope, candidate)
		if err != nil || validated.Draft != candidate {
			return status.Error(codes.Unavailable, "stored task draft is invalid")
		}
		if candidate == run.Draft {
			return nil
		}
		if run.Revision == int64(1<<63-1) {
			return status.Error(codes.FailedPrecondition, "draft revision exhausted")
		}
		result := tx.Exec(`UPDATE agent_task_drafts SET due_at_unix_ms = ?, revision = revision + 1
            WHERE run_id = ? AND item_index = 0`, candidate.DueAtUnixMs, run.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft changed; reload before editing")
		}
		run.Draft, run.Revision = candidate, run.Revision+1
		return nil
	})
	if err != nil {
		return taskDraftRun{}, draftConfirmationStorageError(ctx, err)
	}
	return run, nil
}
