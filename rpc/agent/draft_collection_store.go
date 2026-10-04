package agent

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const insertDraftCollectionRun = `INSERT INTO agent_runs
    (id, team_id, group_id, initiator_id, request_key, request_fingerprint, status, draft_mode, item_count)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const insertDraftCollectionItem = `INSERT INTO agent_task_drafts
    (run_id, item_index, title, description, assignee_id, due_at_unix_ms, source_message_id, assignee_name, assignee_resolution,
     deadline_text, deadline_source, deadline_source_message_id, deadline_reference_unix_ms, deadline_timezone, deadline_resolution, deadline_reason, deadline_parsed_unix_ms, instruction_reference_unix_ms, status)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const selectDraftCollectionForInitiator = `SELECT r.id AS run_id, r.draft_mode, r.item_count, r.status AS run_status,
    r.team_id, r.group_id, r.initiator_id, d.item_index, d.status,
    d.title, d.description, d.assignee_id, d.due_at_unix_ms, d.source_message_id,
    d.task_request_key, d.task_id, d.assignee_name, d.assignee_resolution, d.revision,
    d.deadline_text, d.deadline_source, d.deadline_source_message_id, d.deadline_reference_unix_ms, d.deadline_timezone, d.deadline_resolution, d.deadline_reason, d.deadline_parsed_unix_ms, d.instruction_reference_unix_ms
    FROM agent_runs AS r
    LEFT JOIN agent_task_drafts AS d ON d.run_id = r.id
    WHERE r.id = ? AND r.initiator_id = ? ORDER BY d.item_index LIMIT 6`

type taskDraftCollection struct {
	ID    int64
	Scope draftRunScope
	Items []taskDraftRun
}

type storedDraftCollectionRow struct {
	Draft     storedDraftRow `gorm:"embedded"`
	RunID     int64
	DraftMode string
	ItemCount int
	RunStatus string
	ItemIndex *int32
}

func (s *draftStore) loadDraftCollectionForInitiator(ctx context.Context, runID, actorID int64) (taskDraftCollection, error) {
	if s == nil || s.db == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if runID <= 0 || actorID <= 0 {
		return taskDraftCollection{}, status.Error(codes.InvalidArgument, "invalid run or actor ID")
	}
	return queryDraftCollection(ctx, s.db.WithContext(ctx), runID, actorID, false, nil)
}

func (s *draftStore) loadDraftCollectionForItemEdit(ctx context.Context, runID, actorID int64, index int32) (taskDraftCollection, error) {
	if s == nil || s.db == nil {
		return taskDraftCollection{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	return queryDraftCollection(ctx, s.db.WithContext(ctx), runID, actorID, false, &index)
}

// Both reads and edits use the same complete collection validation. Edits may
// report a known frozen target as a precondition failure, without exposing it.
func queryDraftCollection(ctx context.Context, db *gorm.DB, runID, actorID int64, lock bool, editTarget *int32) (taskDraftCollection, error) {
	query := selectDraftCollectionForInitiator
	if lock {
		query += " FOR UPDATE"
	}
	var rows []storedDraftCollectionRow
	result := db.Raw(query, runID, actorID).Scan(&rows)
	if result.Error != nil {
		return taskDraftCollection{}, draftStorageError(ctx, result.Error)
	}
	if len(rows) == 0 {
		return taskDraftCollection{}, status.Error(codes.NotFound, "draft run not found")
	}
	if rows[0].DraftMode == "single" {
		return taskDraftCollection{}, status.Error(codes.FailedPrecondition, "run is a single draft")
	}
	collection := taskDraftCollection{ID: runID, Scope: rows[0].Draft.run(runID).Scope, Items: make([]taskDraftRun, 0, len(rows))}
	frozenTarget := false
	for i, row := range rows {
		run := row.Draft.run(runID)
		frozen := editTarget != nil && *editTarget == int32(i) && (run.Status == draftCreating || run.Status == draftSucceeded || run.Status == draftSkipped)
		frozenTarget = frozenTarget || frozen
		validated, err := newWaitingTaskDraftRun(run.Scope, run.Draft)
		if row.RunID != runID || row.DraftMode != "collection" || row.ItemCount < 1 || row.ItemCount > maxGeneratedTaskDrafts || row.ItemCount != len(rows) ||
			row.ItemCount != rows[0].ItemCount || row.RunStatus != string(draftWaitingConfirmation) || row.ItemIndex == nil || *row.ItemIndex != int32(i) ||
			run.Scope != collection.Scope || run.Scope.InitiatorID != actorID || run.Revision <= 0 || !validCollectionItemTaskState(run, int32(i)) ||
			run.Draft.AssigneeResolution == "" || run.Draft.Deadline.Resolution == "" || err != nil || validated.Draft != run.Draft {
			return taskDraftCollection{}, status.Error(codes.Unavailable, "stored draft collection is invalid")
		}
		collection.Items = append(collection.Items, run)
	}
	if frozenTarget {
		return taskDraftCollection{}, status.Error(codes.FailedPrecondition, "draft item is not awaiting confirmation")
	}
	return collection, nil
}

func (s *draftStore) saveWaitingDraftCollection(ctx context.Context, runID int64, scope draftRunScope, drafts []taskDraft, requestKey, fingerprint string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if runID <= 0 || !validDraftRequestKey(requestKey) || !validDraftFingerprint(fingerprint) || len(drafts) < 1 || len(drafts) > maxGeneratedTaskDrafts {
		return 0, status.Error(codes.InvalidArgument, "invalid draft collection")
	}
	items, err := prepareWaitingDraftCollection(scope, drafts)
	if err != nil {
		return 0, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return insertWaitingDraftCollection(tx, runID, scope, items, requestKey, fingerprint)
	})
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return s.findReplayedDraft(ctx, scope, requestKey, fingerprint)
		}
		return 0, draftStorageError(ctx, err)
	}
	return runID, nil
}

// Validate and normalize the whole collection before any SQL is executed.
// The trusted caller still owns source authorization and request identity.
func prepareWaitingDraftCollection(scope draftRunScope, drafts []taskDraft) ([]taskDraft, error) {
	if len(drafts) < 1 || len(drafts) > maxGeneratedTaskDrafts {
		return nil, status.Error(codes.InvalidArgument, "invalid draft collection")
	}
	items := make([]taskDraft, len(drafts))
	for i, draft := range drafts {
		run, err := newWaitingTaskDraftRun(scope, draft)
		if err != nil {
			return nil, err
		}
		if run.Draft.AssigneeResolution == "" || run.Draft.Deadline.Resolution == "" {
			return nil, status.Error(codes.InvalidArgument, "draft collection metadata is required")
		}
		items[i] = run.Draft
	}
	return items, nil
}

// Insert already validated items and identity in the caller's transaction.
// In particular, this does not commit, start a transaction, or replay conflicts:
// a background caller must atomically save its inbox result in the same tx.
func insertWaitingDraftCollection(tx *gorm.DB, runID int64, scope draftRunScope, items []taskDraft, requestKey, fingerprint string) error {
	if tx == nil {
		return errors.New("draft collection transaction is required")
	}
	result := tx.Exec(insertDraftCollectionRun, runID, scope.TeamID, scope.GroupID, scope.InitiatorID, requestKey, fingerprint, string(draftWaitingConfirmation), "collection", len(items))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("draft collection run insert affected an invalid number of rows")
	}
	for i, draft := range items {
		d := draft.Deadline
		result := tx.Exec(insertDraftCollectionItem, runID, i, draft.Title, draft.Description, draft.AssigneeID, draft.DueAtUnixMs, draft.SourceMessageID, draft.AssigneeName, string(draft.AssigneeResolution), d.Text, d.Source, d.SourceMessageID, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs, string(draftWaitingConfirmation))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("draft collection item insert affected an invalid number of rows")
		}
	}
	return nil
}
