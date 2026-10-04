package agent

import (
	"context"

	"gorm.io/gorm"
)

const beginTriggerModel = `UPDATE agent_task_trigger_inbox SET model_attempts = model_attempts + 1, model_started = 1 WHERE message_id = ? AND status = 'running' AND lease_token = ? AND lease_until > UTC_TIMESTAMP(6) AND model_started = 0 AND model_attempts < ?`
const releaseTriggerLease = `UPDATE agent_task_trigger_inbox SET status = ?, lease_token = NULL, lease_until = NULL, model_started = 0 WHERE message_id = ? AND status = 'running' AND lease_token = ? AND lease_until > UTC_TIMESTAMP(6)`

// BeginModel grants one call only after its budget transaction commits. A
// repeated or uncertain result never grants permission to invoke the model.
func (s *TriggerInboxStore) BeginModel(ctx context.Context, lease TriggerLease) (bool, error) {
	if err := validateTriggerLeaseInput(ctx, lease); err != nil {
		return false, err
	}
	granted := false
	err := s.triggerLeaseTransaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		saved, err := lockTriggerLease(ctx, tx, lease)
		if err != nil {
			return err
		}
		if saved.ModelStarted == 1 {
			return nil
		}
		if saved.ModelAttempts >= TriggerModelAttemptLimit {
			return ErrInvalidTriggerState
		}
		result := tx.Exec(beginTriggerModel, saved.Event.MessageID, saved.Token.String, TriggerModelAttemptLimit)
		if result.Error != nil {
			return result.Error
		}
		switch result.RowsAffected {
		case 1:
			granted = true
			return nil
		case 0:
			return ErrTriggerLeaseLost
		default:
			return ErrInvalidTriggerState
		}
	})
	if err != nil {
		return false, err
	}
	return granted, nil
}

// Release returns an unspent/first-attempt failure to the queue, or retires a
// spent second attempt. It clears ownership without resetting model budget.
func (s *TriggerInboxStore) Release(ctx context.Context, lease TriggerLease) error {
	if err := validateTriggerLeaseInput(ctx, lease); err != nil {
		return err
	}
	return s.triggerLeaseTransaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		saved, err := lockTriggerLease(ctx, tx, lease)
		if err != nil {
			return err
		}
		next := TriggerInboxQueued
		if saved.ModelAttempts == TriggerModelAttemptLimit {
			next = TriggerInboxExhausted
		}
		result := tx.Exec(releaseTriggerLease, next, saved.Event.MessageID, saved.Token.String)
		if result.Error != nil {
			return result.Error
		}
		switch result.RowsAffected {
		case 1:
			return nil
		case 0:
			return ErrTriggerLeaseLost
		default:
			return ErrInvalidTriggerState
		}
	})
}
