package agent

import (
	"context"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type storedDraftRow struct {
	Revision                   int64
	TeamID                     int64
	GroupID                    int64
	InitiatorID                int64
	Status                     string
	Title                      string
	Description                string
	AssigneeID                 int64
	DueAtUnixMs                int64
	SourceMessageID            int64
	TaskRequestKey             string
	TaskID                     int64
	AssigneeName               string
	AssigneeResolution         draftAssigneeResolution
	DeadlineText               string
	DeadlineSource             string
	DeadlineSourceMessageID    int64
	DeadlineReferenceUnixMs    int64
	DeadlineTimezone           string
	DeadlineResolution         string
	DeadlineReason             string
	DeadlineParsedUnixMs       int64
	InstructionReferenceUnixMs int64
}

func (r storedDraftRow) run(id int64) taskDraftRun {
	return taskDraftRun{
		ID: id, Revision: r.Revision, Scope: draftRunScope{TeamID: r.TeamID, GroupID: r.GroupID, InitiatorID: r.InitiatorID},
		Status: draftRunStatus(r.Status), TaskRequestKey: r.TaskRequestKey, TaskID: r.TaskID,
		Draft: taskDraft{Title: r.Title, Description: r.Description, AssigneeID: r.AssigneeID,
			DueAtUnixMs: r.DueAtUnixMs, SourceMessageID: r.SourceMessageID,
			AssigneeName: r.AssigneeName, AssigneeResolution: r.AssigneeResolution,
			Deadline: draftDeadlineMetadata{Text: r.DeadlineText, Source: r.DeadlineSource, SourceMessageID: r.DeadlineSourceMessageID, ReferenceUnixMs: r.DeadlineReferenceUnixMs, Timezone: r.DeadlineTimezone, Resolution: r.DeadlineResolution, Reason: r.DeadlineReason, ParsedUnixMs: r.DeadlineParsedUnixMs, InstructionReferenceUnixMs: r.InstructionReferenceUnixMs}},
	}
}

func draftTaskRequestKey(runID int64) string {
	return "agent-task-" + strconv.FormatInt(runID, 10) + "-0"
}

// lockedDraft uses the same run/draft row locks as text edits. No Task RPC is
// executed within this transaction; committed state survives response loss.
func lockedDraft(tx *gorm.DB, runID, actorID int64) (taskDraftRun, error) {
	var row storedDraftRow
	result := tx.Raw(selectDraftForInitiator+" FOR UPDATE", runID, actorID).Scan(&row)
	if result.Error != nil {
		return taskDraftRun{}, result.Error
	}
	if result.RowsAffected == 0 || row.InitiatorID != actorID {
		return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
	}
	if row.Status == "collection" {
		return taskDraftRun{}, status.Error(codes.FailedPrecondition, "use the draft collection interface")
	}
	run := row.run(runID)
	if !run.Draft.Deadline.valid(run.Draft.DueAtUnixMs) {
		return taskDraftRun{}, status.Error(codes.Unavailable, "stored deadline metadata is invalid")
	}
	return run, nil
}

// freezeDraft atomically compares the saved text and reserves a stable Task
// request. Creating/succeeded replays never change its key or any task field.
func (s *draftStore) freezeDraft(ctx context.Context, runID, actorID int64, expectedTitle, expectedDescription string, expectedRevision int64, expectedAssigneeID, expectedDueAtUnixMs *int64, expectedDeadlineResolution string) (taskDraftRun, error) {
	if s == nil || s.db == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if runID <= 0 || actorID <= 0 || expectedRevision <= 0 || (expectedAssigneeID != nil && *expectedAssigneeID < 0) ||
		(expectedDueAtUnixMs != nil && (*expectedDueAtUnixMs < 0 || *expectedDueAtUnixMs > maxDraftDueAtUnixMs)) || !validDeadlineResolution(expectedDeadlineResolution) {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid run or actor ID")
	}
	var run taskDraftRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		run, err = lockedDraft(tx, runID, actorID)
		if err != nil {
			return err
		}
		if run.Revision <= 0 {
			return status.Error(codes.Unavailable, "stored draft revision is invalid")
		}
		if run.Revision != expectedRevision || run.Draft.Title != expectedTitle || run.Draft.Description != expectedDescription {
			return status.Error(codes.Aborted, "draft changed; reload before confirming")
		}
		validated, err := newWaitingTaskDraftRun(run.Scope, run.Draft)
		if err != nil || validated.Draft != run.Draft {
			return status.Error(codes.Unavailable, "stored task draft is invalid")
		}
		if err := run.Draft.requireAssigneeReview(expectedAssigneeID); err != nil {
			return err
		}
		if err := run.Draft.requireDeadlineStateReview(expectedDueAtUnixMs, expectedDeadlineResolution); err != nil {
			return err
		}
		key := draftTaskRequestKey(runID)
		switch run.Status {
		case draftCreating, draftSucceeded:
			if run.TaskRequestKey != key || (run.Status == draftCreating && run.TaskID != 0) ||
				(run.Status == draftSucceeded && run.TaskID <= 0) {
				return status.Error(codes.Unavailable, "stored task result is invalid")
			}
			return nil
		case draftWaitingConfirmation:
			if run.TaskRequestKey != "" || run.TaskID != 0 {
				return status.Error(codes.Unavailable, "stored task result is invalid")
			}
		default:
			return status.Error(codes.FailedPrecondition, "draft cannot be confirmed")
		}
		result := tx.Exec(`UPDATE agent_task_drafts SET task_request_key = ?
            WHERE run_id = ? AND item_index = 0`, key, runID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft changed; reload before confirming")
		}
		result = tx.Exec(`UPDATE agent_runs SET status = ? WHERE id = ? AND initiator_id = ?`,
			string(draftCreating), runID, actorID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft changed; reload before confirming")
		}
		run.Status, run.TaskRequestKey = draftCreating, key
		return nil
	})
	if err != nil {
		return taskDraftRun{}, draftConfirmationStorageError(ctx, err)
	}
	return run, nil
}

// completeDraft only advances creating -> succeeded. A late failed RPC has no
// storage transition; duplicate successful responses must agree on task ID.
func (s *draftStore) completeDraft(ctx context.Context, runID, actorID int64, key string, taskID int64) (taskDraftRun, error) {
	if s == nil || s.db == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if runID <= 0 || actorID <= 0 || taskID <= 0 || key != draftTaskRequestKey(runID) {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid task result")
	}
	var run taskDraftRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		run, err = lockedDraft(tx, runID, actorID)
		if err != nil {
			return err
		}
		if run.TaskRequestKey != key {
			return status.Error(codes.FailedPrecondition, "draft task request does not match")
		}
		if run.Status == draftSucceeded {
			if run.TaskID != taskID {
				return status.Error(codes.Internal, "task result does not match")
			}
			return nil
		}
		if run.Status != draftCreating || run.TaskID != 0 {
			return status.Error(codes.FailedPrecondition, "draft is not creating a task")
		}
		result := tx.Exec(`UPDATE agent_task_drafts SET task_id = ? WHERE run_id = ? AND item_index = 0`, taskID, runID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft task result changed")
		}
		result = tx.Exec(`UPDATE agent_runs SET status = ? WHERE id = ? AND initiator_id = ?`,
			string(draftSucceeded), runID, actorID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft task result changed")
		}
		run.Status, run.TaskID = draftSucceeded, taskID
		return nil
	})
	if err != nil {
		return taskDraftRun{}, draftConfirmationStorageError(ctx, err)
	}
	return run, nil
}

func draftConfirmationStorageError(ctx context.Context, err error) error {
	if status.Code(err) != codes.Unknown {
		return err
	}
	return draftStorageError(ctx, err)
}
