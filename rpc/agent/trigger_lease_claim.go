package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"gorm.io/gorm"
)

const selectClaimableTriggerLease = `SELECT ` + triggerExecutionColumns + ` FROM agent_task_trigger_inbox
    WHERE status = 'queued' OR (status = 'running' AND lease_until <= UTC_TIMESTAMP(6))
    ORDER BY message_id LIMIT 1 FOR UPDATE SKIP LOCKED`

const claimTriggerLeaseSQL = `UPDATE agent_task_trigger_inbox
    SET status = 'running', lease_token = ?, lease_until = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 30 SECOND), model_started = 0
    WHERE message_id = ? AND status = ? AND model_attempts = ? AND model_started = ?
    AND ((status = 'queued' AND lease_token IS NULL AND lease_until IS NULL)
         OR (status = 'running' AND lease_token = ? AND lease_until <= UTC_TIMESTAMP(6)))`

const retireExpiredTriggerLeaseSQL = `UPDATE agent_task_trigger_inbox
    SET status = 'exhausted', lease_token = NULL, lease_until = NULL, model_started = 0
    WHERE message_id = ? AND status = 'running' AND lease_token = ? AND model_attempts = 2 AND lease_until <= UTC_TIMESTAMP(6)`

const renewTriggerLeaseSQL = `UPDATE agent_task_trigger_inbox
    SET lease_until = GREATEST(DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 30 SECOND), DATE_ADD(lease_until, INTERVAL 1 MICROSECOND))
    WHERE message_id = ? AND status = 'running' AND lease_token = ? AND lease_until > UTC_TIMESTAMP(6)`

func randomTriggerLeaseToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func triggerLeaseAffectedRows(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrTriggerLeaseLost
	}
	if result.RowsAffected != 1 {
		return ErrInvalidTriggerState
	}
	return nil
}

func (s *TriggerInboxStore) Claim(ctx context.Context) (*TriggerLease, error) {
	return s.claimWithToken(ctx, randomTriggerLeaseToken)
}

// Only this private seam accepts a token generator, so tests can prove entropy
// failures leave the row untouched. Production always uses crypto/rand.
func (s *TriggerInboxStore) claimWithToken(ctx context.Context, token func() (string, error)) (*TriggerLease, error) {
	var claimed *TriggerLease
	err := s.triggerLeaseTransaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := readTriggerExecutionRow(ctx, tx, selectClaimableTriggerLease)
		if err != nil || row == nil {
			return err
		}
		if row.Status != TriggerInboxQueued && row.Status != TriggerInboxRunning {
			return ErrInvalidTriggerState
		}
		if row.Status == TriggerInboxRunning && row.ModelAttempts == TriggerModelAttemptLimit {
			return triggerLeaseAffectedRows(tx.Exec(retireExpiredTriggerLeaseSQL, row.Event.MessageID, row.Token.String))
		}
		if token == nil {
			return errors.New("trigger lease entropy unavailable")
		}
		newToken, err := token()
		if err != nil || !validTriggerLeaseToken(newToken) || newToken == row.Token.String {
			return errors.New("trigger lease entropy unavailable")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := triggerLeaseAffectedRows(tx.Exec(claimTriggerLeaseSQL, newToken, row.Event.MessageID, row.Status, row.ModelAttempts, row.ModelStarted, row.Token.String)); err != nil {
			return err
		}
		live, err := lockTriggerLease(ctx, tx, TriggerLease{MessageID: row.Event.MessageID, Token: newToken})
		if err != nil {
			return err
		}
		if live.ModelAttempts != row.ModelAttempts || live.ModelStarted != 0 {
			return ErrInvalidTriggerState
		}
		claimed = live.lease()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *TriggerInboxStore) Renew(ctx context.Context, lease TriggerLease) (*TriggerLease, error) {
	if err := validateTriggerLeaseInput(ctx, lease); err != nil {
		return nil, err
	}
	var renewed *TriggerLease
	err := s.triggerLeaseTransaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		row, err := lockTriggerLease(ctx, tx, lease)
		if err != nil {
			return err
		}
		if err := triggerLeaseAffectedRows(tx.Exec(renewTriggerLeaseSQL, lease.MessageID, lease.Token)); err != nil {
			return err
		}
		live, err := lockTriggerLease(ctx, tx, lease)
		if err != nil {
			return err
		}
		if live.ModelAttempts != row.ModelAttempts || live.ModelStarted != row.ModelStarted {
			return ErrInvalidTriggerState
		}
		renewed = live.lease()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return renewed, nil
}
