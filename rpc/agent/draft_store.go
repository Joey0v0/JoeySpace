package agent

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type draftStore struct{ db *gorm.DB }

const selectDraftForInitiator = `SELECT r.team_id, r.group_id, r.initiator_id, CASE WHEN r.draft_mode = 'single' THEN r.status ELSE 'collection' END AS status,
    d.title, d.description, d.assignee_id, d.due_at_unix_ms, d.source_message_id,
    d.task_request_key, d.task_id, d.assignee_name, d.assignee_resolution, d.revision,
    d.deadline_text, d.deadline_source, d.deadline_source_message_id, d.deadline_reference_unix_ms, d.deadline_timezone, d.deadline_resolution, d.deadline_reason, d.deadline_parsed_unix_ms, d.instruction_reference_unix_ms
    FROM agent_runs AS r
    JOIN agent_task_drafts AS d ON d.run_id = r.id AND d.item_index = 0
    WHERE r.id = ? AND r.initiator_id = ?`

// saveWaitingDraft persists one run and its first draft atomically. The scope
// and request fingerprint must come from the trusted Agent flow. Replaying the
// same initiator/key/request returns the original run ID.
func (s *draftStore) saveWaitingDraft(ctx context.Context, runID int64, scope draftRunScope, draft taskDraft, requestKey, fingerprint string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if runID <= 0 {
		return 0, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	if !validDraftRequestKey(requestKey) || !validDraftFingerprint(fingerprint) {
		return 0, status.Error(codes.InvalidArgument, "invalid draft request identity")
	}
	run, err := newWaitingTaskDraftRun(scope, draft)
	if err != nil {
		return 0, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO agent_runs
            (id, team_id, group_id, initiator_id, request_key, request_fingerprint, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)`, runID, run.Scope.TeamID, run.Scope.GroupID,
			run.Scope.InitiatorID, requestKey, fingerprint, string(run.Status)).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO agent_task_drafts
            (run_id, item_index, title, description, assignee_id, due_at_unix_ms, source_message_id, assignee_name, assignee_resolution,
             deadline_text, deadline_source, deadline_source_message_id, deadline_reference_unix_ms, deadline_timezone, deadline_resolution, deadline_reason, deadline_parsed_unix_ms, instruction_reference_unix_ms)
            VALUES (?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, run.Draft.Title,
			run.Draft.Description, run.Draft.AssigneeID, run.Draft.DueAtUnixMs,
			run.Draft.SourceMessageID, run.Draft.AssigneeName, string(run.Draft.AssigneeResolution),
			run.Draft.Deadline.Text, run.Draft.Deadline.Source, run.Draft.Deadline.SourceMessageID, run.Draft.Deadline.ReferenceUnixMs, run.Draft.Deadline.Timezone, run.Draft.Deadline.Resolution, run.Draft.Deadline.Reason, run.Draft.Deadline.ParsedUnixMs, run.Draft.Deadline.InstructionReferenceUnixMs).Error
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

func (s *draftStore) findReplayedDraft(ctx context.Context, scope draftRunScope, requestKey, fingerprint string) (int64, error) {
	id, err := s.findExistingDraft(ctx, scope, requestKey, fingerprint)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, status.Error(codes.AlreadyExists, "draft run ID already exists")
	}
	return id, nil
}

func (s *draftStore) findExistingDraft(ctx context.Context, scope draftRunScope, requestKey, fingerprint string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	var previous struct {
		ID                 int64
		TeamID             int64
		GroupID            int64
		RequestFingerprint string
	}
	result := s.db.WithContext(ctx).Raw(`SELECT id, team_id, group_id, request_fingerprint FROM agent_runs
        WHERE initiator_id = ? AND request_key = ?`, scope.InitiatorID, requestKey).Scan(&previous)
	if result.Error != nil {
		return 0, draftStorageError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, nil
	}
	if previous.ID <= 0 {
		return 0, status.Error(codes.Unavailable, "draft storage unavailable")
	}
	if previous.TeamID != scope.TeamID || previous.GroupID != scope.GroupID || previous.RequestFingerprint != fingerprint {
		return 0, status.Error(codes.AlreadyExists, "draft request key used for another request")
	}
	return previous.ID, nil
}

func validDraftRequestKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validDraftFingerprint(fingerprint string) bool {
	if len(fingerprint) != 64 {
		return false
	}
	_, err := hex.DecodeString(fingerprint)
	return err == nil
}

// loadDraftForInitiator filters by both run ID and the authenticated actor ID.
// Token resolution and current team/group membership checks belong to the RPC
// handler and are not performed by this storage method.
func (s *draftStore) loadDraftForInitiator(ctx context.Context, runID, actorID int64) (taskDraftRun, error) {
	if s == nil || s.db == nil {
		return taskDraftRun{}, status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if runID <= 0 || actorID <= 0 {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid run or actor ID")
	}
	var row storedDraftRow
	result := s.db.WithContext(ctx).Raw(selectDraftForInitiator, runID, actorID).Scan(&row)
	if result.Error != nil {
		return taskDraftRun{}, draftStorageError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		// A missing run and another user's run are indistinguishable to callers.
		return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
	}
	if row.Status == "collection" {
		return taskDraftRun{}, status.Error(codes.FailedPrecondition, "use the draft collection interface")
	}
	run := row.run(runID)
	if !run.Draft.Deadline.valid(run.Draft.DueAtUnixMs) {
		return taskDraftRun{}, status.Error(codes.Unavailable, "stored deadline metadata is invalid")
	}
	if run.Revision <= 0 {
		return taskDraftRun{}, status.Error(codes.Unavailable, "stored draft revision is invalid")
	}
	if err := run.requireInitiator(actorID); err != nil {
		return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
	}
	return run, nil
}

// updateDraftText locks the run and its first draft, then checks that neither
// status nor text changed since the authorized read. A stale edit cannot
// overwrite another edit or a future confirmation.
func (s *draftStore) updateDraftText(ctx context.Context, run taskDraftRun, title, description string) error {
	if s == nil || s.db == nil {
		return status.Error(codes.Unavailable, "draft storage is not configured")
	}
	if run.ID <= 0 || run.Scope.InitiatorID <= 0 || run.Revision <= 0 {
		return status.Error(codes.InvalidArgument, "invalid run or actor ID")
	}
	if err := run.requireWaitingConfirmation(run.Scope.InitiatorID); err != nil {
		return err
	}
	candidate := run.Draft
	candidate.Title, candidate.Description = title, description
	validated, err := newWaitingTaskDraftRun(run.Scope, candidate)
	if err != nil {
		return err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current struct {
			Status      string
			Title       string
			Description string
			Revision    int64
		}
		result := tx.Raw(`SELECT CASE WHEN r.draft_mode = 'single' THEN r.status ELSE 'collection' END AS status, d.title, d.description, d.revision FROM agent_runs AS r
            JOIN agent_task_drafts AS d ON d.run_id = r.id AND d.item_index = 0
            WHERE r.id = ? AND r.initiator_id = ? FOR UPDATE`, run.ID, run.Scope.InitiatorID).Scan(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return status.Error(codes.NotFound, "draft run not found")
		}
		if current.Status != string(draftWaitingConfirmation) {
			return status.Error(codes.FailedPrecondition, "draft is not awaiting confirmation")
		}
		if current.Revision != run.Revision || current.Title != run.Draft.Title || current.Description != run.Draft.Description {
			return status.Error(codes.Aborted, "draft changed; reload before editing")
		}
		if current.Title == validated.Draft.Title && current.Description == validated.Draft.Description {
			return nil
		}
		if current.Revision == int64(1<<63-1) {
			return status.Error(codes.FailedPrecondition, "draft revision exhausted")
		}
		result = tx.Exec(`UPDATE agent_task_drafts SET title = ?, description = ?, revision = revision + 1
            WHERE run_id = ? AND item_index = 0`, validated.Draft.Title, validated.Draft.Description, run.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Aborted, "draft changed; reload before editing")
		}
		return nil
	})
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return err
		}
		return draftStorageError(ctx, err)
	}
	return nil
}

func draftStorageError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return status.Error(codes.AlreadyExists, "draft run already exists")
	}
	return status.Error(codes.Unavailable, "draft storage unavailable")
}
