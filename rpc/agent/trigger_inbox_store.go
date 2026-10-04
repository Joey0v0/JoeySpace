package agent

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const triggerInboxSQLTimeout = 3 * time.Second
const triggerInboxColumns = `message_id, action, event_version, status, received_at`
const insertTriggerInbox = `INSERT INTO agent_task_trigger_inbox (message_id, action, event_version, status) VALUES (?, ?, ?, ?)`
const selectTriggerInboxForUpdate = `SELECT ` + triggerInboxColumns + `, result_run_id FROM agent_task_trigger_inbox WHERE message_id = ? FOR UPDATE`

var errInvalidTriggerInboxReceipt = errors.New("saved trigger notification receipt is invalid")

// TriggerInboxStore owns notification receipt and execution state. Accept only
// verifies immutable receipt facts; it never resets an existing lease or budget,
// authorizes generation, or derives an instruction reference from received_at.
type TriggerInboxStore struct {
	db *gorm.DB
}

func NewTriggerInboxStore(db *gorm.DB) *TriggerInboxStore { return &TriggerInboxStore{db: db} }

func (s *TriggerInboxStore) Accept(ctx context.Context, event model.AgentTriggerEvent) error {
	if ctx == nil {
		return status.Error(codes.InvalidArgument, "trigger notification context required")
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if err := validateTriggerNotification(event); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return status.Error(codes.Unavailable, "trigger inbox storage unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerInboxSQLTimeout)
	defer cancel()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result := tx.Exec(insertTriggerInbox, event.MessageID, event.Action, event.Version, TriggerInboxQueued)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if result.Error == nil {
			if result.RowsAffected != 1 {
				return errors.New("trigger inbox insertion affected invalid record count")
			}
			return nil
		}
		var duplicate *mysql.MySQLError
		if !errors.As(result.Error, &duplicate) || duplicate.Number != 1062 {
			return result.Error
		}
		return checkTriggerInboxReceipt(ctx, tx, event)
	})
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, errInvalidTriggerInboxReceipt) {
		return status.Error(codes.FailedPrecondition, "saved trigger notification does not match immutable receipt facts")
	}
	if err != nil {
		return status.Error(codes.Unavailable, "trigger inbox storage unavailable")
	}
	return nil
}

// Execution fields are checked by the lease store. Notification replay only
// checks this receipt, including a known status, and never writes the old row.
func checkTriggerInboxReceipt(ctx context.Context, tx *gorm.DB, expected model.AgentTriggerEvent) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	rows, err := tx.Raw(selectTriggerInboxForUpdate, expected.MessageID).Rows()
	if ctx.Err() != nil {
		if rows != nil {
			rows.Close()
		}
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var saved model.AgentTriggerEvent
		var savedStatus string
		var receivedAt time.Time
		var resultRunID sql.NullInt64
		if found || rows.Scan(&saved.MessageID, &saved.Action, &saved.Version, &savedStatus, &receivedAt, &resultRunID) != nil ||
			saved != expected || validateTriggerNotification(saved) != nil || !validTriggerInboxReceiptStatus(savedStatus) || !validTriggerInboxResult(savedStatus, resultRunID) || receivedAt.IsZero() || receivedAt.UnixMilli() <= 0 {
			return errInvalidTriggerInboxReceipt
		}
		found = true
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	if !found {
		return errInvalidTriggerInboxReceipt
	}
	return nil
}
