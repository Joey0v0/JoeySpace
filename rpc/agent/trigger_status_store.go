package agent

import (
	"context"
	"database/sql"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const selectTaskTriggerStatus = `SELECT message_id, status, result_run_id FROM agent_task_trigger_inbox WHERE message_id = ?`

type taskTriggerStatus struct {
	MessageID int64
	Status    string
	RunID     int64
}

func (s taskTriggerStatus) valid(messageID int64) bool {
	return messageID > 0 && s.MessageID == messageID && validTriggerInboxReceiptStatus(s.Status) &&
		(s.Status == TriggerInboxCompleted && s.RunID > 0 || s.Status != TriggerInboxCompleted && s.RunID == 0)
}

// A read never locks or mutates the execution row, including retry/lease fields.
func (s *TriggerInboxStore) loadTaskTriggerStatus(ctx context.Context, messageID int64) (taskTriggerStatus, error) {
	if ctx == nil {
		return taskTriggerStatus{}, status.Error(codes.InvalidArgument, "trigger status context required")
	}
	if ctx.Err() != nil {
		return taskTriggerStatus{}, status.FromContextError(ctx.Err()).Err()
	}
	if messageID <= 0 {
		return taskTriggerStatus{}, status.Error(codes.InvalidArgument, "positive trigger message ID required")
	}
	if s == nil || s.db == nil {
		return taskTriggerStatus{}, status.Error(codes.Unavailable, "trigger status storage unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerInboxSQLTimeout)
	defer cancel()
	rows, err := s.db.WithContext(ctx).Raw(selectTaskTriggerStatus, messageID).Rows()
	if ctx.Err() != nil || err != nil {
		if rows != nil {
			rows.Close()
		}
		return taskTriggerStatus{}, taskTriggerStatusStorageError(ctx)
	}
	defer rows.Close()
	var result taskTriggerStatus
	found := false
	for rows.Next() {
		var runID sql.NullInt64
		if found || rows.Scan(&result.MessageID, &result.Status, &runID) != nil ||
			!validTriggerInboxResult(result.Status, runID) {
			return taskTriggerStatus{}, taskTriggerStatusStorageError(ctx)
		}
		result.RunID = runID.Int64
		if !result.valid(messageID) {
			return taskTriggerStatus{}, taskTriggerStatusStorageError(ctx)
		}
		found = true
	}
	if ctx.Err() != nil || rows.Err() != nil || rows.Close() != nil {
		return taskTriggerStatus{}, taskTriggerStatusStorageError(ctx)
	}
	if !found {
		return taskTriggerStatus{}, status.Error(codes.NotFound, "task trigger not found")
	}
	return result, nil
}

func taskTriggerStatusStorageError(ctx context.Context) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Error(codes.Unavailable, "trigger status storage unavailable")
}
