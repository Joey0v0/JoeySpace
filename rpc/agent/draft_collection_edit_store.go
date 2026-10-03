package agent

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const updateDraftCollectionText = `UPDATE agent_task_drafts SET title = ?, description = ?, revision = revision + 1
    WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'`

const updateDraftCollectionAssignee = `UPDATE agent_task_drafts SET assignee_id = ?, assignee_resolution = ?, revision = revision + 1
    WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'`

const updateDraftCollectionDeadline = `UPDATE agent_task_drafts SET due_at_unix_ms = ?, deadline_resolution = ?, revision = revision + 1
    WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'`

func (s *draftStore) updateDraftCollectionItem(ctx context.Context, authorized taskDraftCollection, index int32, candidate taskDraft, kind draftCollectionEditKind) (taskDraftCollection, error) {
	if s == nil || s.db == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if authorized.ID <= 0 || index < 0 || int(index) >= len(authorized.Items) || len(authorized.Items) > maxGeneratedTaskDrafts {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item identity")
	}
	expected := authorized.Items[index]
	if expected.Revision <= 0 {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid draft item revision")
	}
	validated, err := newWaitingTaskDraftRun(authorized.Scope, candidate)
	if err != nil {
		return taskDraftCollection{}, err
	}
	var saved taskDraftCollection
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := queryDraftCollection(ctx, tx, authorized.ID, authorized.Scope.InitiatorID, true, &index)
		if err != nil {
			return err
		}
		if current.Scope != authorized.Scope || len(current.Items) != len(authorized.Items) || int(index) >= len(current.Items) || current.Items[index] != expected {
			return status.Error(codes.Aborted, "draft item changed; reload before editing")
		}
		// An allowed edit cannot change evidence or unrelated fields, even if an
		// internal caller accidentally passes a broader candidate.
		allowed := expected.Draft
		switch kind {
		case collectionEditText:
			allowed.Title, allowed.Description = validated.Draft.Title, validated.Draft.Description
		case collectionEditAssignee:
			allowed.AssigneeID, allowed.AssigneeResolution = validated.Draft.AssigneeID, validated.Draft.AssigneeResolution
			state := assigneeUnassigned
			if allowed.AssigneeID > 0 {
				state = assigneeSelected
			}
			if allowed.AssigneeResolution != state {
				return status.Error(codes.InvalidArgument, "invalid assignee selection state")
			}
		case collectionEditDeadline:
			allowed.DueAtUnixMs, allowed.Deadline.Resolution = validated.Draft.DueAtUnixMs, validated.Draft.Deadline.Resolution
			state := "unset"
			if allowed.DueAtUnixMs > 0 {
				state = "selected"
			}
			if allowed.Deadline.Resolution != state {
				return status.Error(codes.InvalidArgument, "invalid deadline selection state")
			}
		default:
			return status.Error(codes.InvalidArgument, "invalid draft item edit")
		}
		if allowed != validated.Draft {
			return status.Error(codes.InvalidArgument, "draft item edit changed unrelated fields")
		}
		if current.Items[index].Draft == validated.Draft {
			saved = current
			return nil
		}
		if expected.Revision == int64(1<<63-1) {
			return status.Error(codes.FailedPrecondition, "draft revision exhausted")
		}
		var result *gorm.DB
		switch kind {
		case collectionEditText:
			result = tx.Exec(updateDraftCollectionText, validated.Draft.Title, validated.Draft.Description, authorized.ID, index, expected.Revision)
		case collectionEditAssignee:
			result = tx.Exec(updateDraftCollectionAssignee, validated.Draft.AssigneeID, string(validated.Draft.AssigneeResolution), authorized.ID, index, expected.Revision)
		case collectionEditDeadline:
			result = tx.Exec(updateDraftCollectionDeadline, validated.Draft.DueAtUnixMs, validated.Draft.Deadline.Resolution, authorized.ID, index, expected.Revision)
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft item changed; reload before editing")
		}
		current.Items[index].Draft = validated.Draft
		current.Items[index].Revision++
		saved = current
		return nil
	})
	if err != nil {
		return taskDraftCollection{}, draftConfirmationStorageError(ctx, err)
	}
	return saved, nil
}
