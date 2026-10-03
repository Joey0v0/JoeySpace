package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// Membership was checked before opening this transaction. The shared row locks
// and revision prevent choosing against a concurrently edited or frozen draft.
func (s *draftStore) updateDraftAssignee(ctx context.Context, authorized taskDraftRun, assigneeID int64) (taskDraftRun, error) {
	if s == nil || s.db == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if authorized.ID <= 0 || authorized.Scope.InitiatorID <= 0 || authorized.Revision <= 0 || assigneeID < 0 {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid assignee selection")
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
			return status.Error(codes.Aborted, "draft changed; reload before choosing")
		}
		current, err := newWaitingTaskDraftRun(run.Scope, run.Draft)
		if err != nil || current.Draft != run.Draft {
			return status.Error(codes.Unavailable, "stored task draft is invalid")
		}
		candidate := run.Draft
		candidate.AssigneeID, candidate.AssigneeResolution = assigneeID, assigneeSelected
		if assigneeID == 0 {
			candidate.AssigneeResolution = assigneeUnassigned
		}
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
		result := tx.Exec(`UPDATE agent_task_drafts SET assignee_id = ?, assignee_resolution = ?, revision = revision + 1
            WHERE run_id = ? AND item_index = 0`, candidate.AssigneeID, string(candidate.AssigneeResolution), run.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft changed; reload before choosing")
		}
		run.Draft, run.Revision = candidate, run.Revision+1
		return nil
	})
	if err != nil {
		return taskDraftRun{}, draftConfirmationStorageError(ctx, err)
	}
	return run, nil
}
