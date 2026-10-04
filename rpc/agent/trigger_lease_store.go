package agent

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const triggerExecutionColumns = triggerInboxColumns + `, lease_token, lease_until, model_attempts, model_started, result_run_id, retry_after, retry_failures`
const triggerRetryFailureLimit = 8
const selectLiveTriggerLease = `SELECT ` + triggerExecutionColumns + ` FROM agent_task_trigger_inbox WHERE message_id = ? AND status = 'running' AND lease_token = ? AND lease_until > UTC_TIMESTAMP(6) FOR UPDATE`

type triggerExecutionRow struct {
	Event         model.AgentTriggerEvent
	Status        string
	ReceivedAt    time.Time
	Token         sql.NullString
	Until         sql.NullTime
	ModelAttempts int
	ModelStarted  int
	ResultRunID   sql.NullInt64
	RetryAfter    sql.NullTime
	RetryFailures int
}

func validTriggerLeaseToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, c := range token {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateTriggerExecutionRow(row *triggerExecutionRow) error {
	if row == nil || validateTriggerNotification(row.Event) != nil || row.ReceivedAt.IsZero() || row.ReceivedAt.UnixMilli() <= 0 ||
		row.ModelAttempts < 0 || row.ModelAttempts > TriggerModelAttemptLimit || row.ModelStarted < 0 || row.ModelStarted > 1 ||
		!validTriggerInboxResult(row.Status, row.ResultRunID) {
		return ErrInvalidTriggerState
	}
	if row.RetryFailures < 0 || row.RetryFailures > triggerRetryFailureLimit ||
		row.RetryAfter.Valid && (row.Status != TriggerInboxQueued || row.RetryFailures == 0 || row.RetryAfter.Time.IsZero() || row.RetryAfter.Time.UnixMilli() <= 0 || row.RetryAfter.Time.Year() > 9999) ||
		row.Status == TriggerInboxQueued && row.RetryFailures > 0 && !row.RetryAfter.Valid {
		return ErrInvalidTriggerState
	}
	switch row.Status {
	case TriggerInboxCompleted:
		if row.Token.Valid || row.Until.Valid || row.ModelStarted != 0 || row.ModelAttempts < 1 {
			return ErrInvalidTriggerState
		}
	case TriggerInboxQueued, TriggerInboxExhausted:
		if row.Token.Valid || row.Until.Valid || row.ModelStarted != 0 ||
			(row.Status == TriggerInboxQueued && row.ModelAttempts >= TriggerModelAttemptLimit) ||
			(row.Status == TriggerInboxExhausted && row.ModelAttempts != TriggerModelAttemptLimit) {
			return ErrInvalidTriggerState
		}
	case TriggerInboxRunning:
		if !row.Token.Valid || !validTriggerLeaseToken(row.Token.String) || !row.Until.Valid || row.Until.Time.IsZero() || row.Until.Time.UnixMilli() <= 0 ||
			(row.ModelStarted == 1 && row.ModelAttempts == 0) || (row.ModelStarted == 0 && row.ModelAttempts == TriggerModelAttemptLimit) {
			return ErrInvalidTriggerState
		}
	default:
		return ErrInvalidTriggerState
	}
	return nil
}

func (row *triggerExecutionRow) lease() *TriggerLease {
	return &TriggerLease{MessageID: row.Event.MessageID, Token: row.Token.String, Until: row.Until.Time, ModelAttempts: row.ModelAttempts, ModelStarted: row.ModelStarted == 1}
}

// Exactly one strictly validated row, or nil. Never derives validity from the
// worker's wall clock: live/expired predicates are part of each SQL query.
func readTriggerExecutionRow(ctx context.Context, tx *gorm.DB, query string, args ...any) (*triggerExecutionRow, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	rows, err := tx.Raw(query, args...).Rows()
	if ctx.Err() != nil {
		if rows != nil {
			rows.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result *triggerExecutionRow
	for rows.Next() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		row := &triggerExecutionRow{}
		if result != nil || rows.Scan(&row.Event.MessageID, &row.Event.Action, &row.Event.Version, &row.Status, &row.ReceivedAt,
			&row.Token, &row.Until, &row.ModelAttempts, &row.ModelStarted, &row.ResultRunID, &row.RetryAfter, &row.RetryFailures) != nil || validateTriggerExecutionRow(row) != nil {
			return nil, ErrInvalidTriggerState
		}
		result = row
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return result, nil
}

func lockTriggerLease(ctx context.Context, tx *gorm.DB, lease TriggerLease) (*triggerExecutionRow, error) {
	row, err := readTriggerExecutionRow(ctx, tx, selectLiveTriggerLease, lease.MessageID, lease.Token)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrTriggerLeaseLost
	}
	if row.Event.MessageID != lease.MessageID || row.Status != TriggerInboxRunning || row.Token.String != lease.Token {
		return nil, ErrInvalidTriggerState
	}
	return row, nil
}

func validateTriggerLeaseInput(ctx context.Context, lease TriggerLease) error {
	if ctx == nil {
		return status.Error(codes.InvalidArgument, "trigger execution context required")
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if lease.MessageID <= 0 || !validTriggerLeaseToken(lease.Token) {
		return status.Error(codes.InvalidArgument, "invalid trigger execution lease")
	}
	return nil
}

func (s *TriggerInboxStore) triggerLeaseTransaction(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	if ctx == nil {
		return status.Error(codes.InvalidArgument, "trigger execution context required")
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if s == nil || s.db == nil {
		return status.Error(codes.Unavailable, "trigger execution storage unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerInboxSQLTimeout)
	defer cancel()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fn(ctx, tx)
	})
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, ErrTriggerLeaseLost) {
		return ErrTriggerLeaseLost
	}
	if errors.Is(err, ErrInvalidTriggerState) {
		return ErrInvalidTriggerState
	}
	if err != nil {
		return status.Error(codes.Unavailable, "trigger execution storage unavailable")
	}
	return nil
}

// Future draft result persistence must share this transaction with its fence.
// Both checks use DB time while holding the same row lock; an expired or newer
// owner cannot interleave. Callback must contain only short SQL, never RPC/model.
func (s *TriggerInboxStore) withTriggerLease(ctx context.Context, lease TriggerLease, mutation func(context.Context, *gorm.DB) error) error {
	if err := validateTriggerLeaseInput(ctx, lease); err != nil {
		return err
	}
	if mutation == nil {
		return status.Error(codes.InvalidArgument, "trigger execution mutation required")
	}
	return s.triggerLeaseTransaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if _, err := lockTriggerLease(ctx, tx, lease); err != nil {
			return err
		}
		if err := mutation(ctx, tx); err != nil {
			return err
		}
		_, err := lockTriggerLease(ctx, tx, lease)
		return err
	})
}

func validTriggerInboxReceiptStatus(value string) bool {
	return value == TriggerInboxQueued || value == TriggerInboxRunning || value == TriggerInboxExhausted || value == TriggerInboxCompleted
}

func validTriggerInboxResult(state string, result sql.NullInt64) bool {
	if state == TriggerInboxCompleted {
		return result.Valid && result.Int64 > 0
	}
	return !result.Valid
}
