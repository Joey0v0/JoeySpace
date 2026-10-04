package agent

import (
	"context"

	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const completeTriggerDraftCollection = `UPDATE agent_task_trigger_inbox
    SET status = 'completed', result_run_id = ?, lease_token = NULL, lease_until = NULL, model_started = 0
    WHERE message_id = ? AND status = 'running' AND lease_token = ? AND lease_until > UTC_TIMESTAMP(6)
    AND model_started = 1 AND model_attempts = ? AND model_attempts BETWEEN 1 AND 2 AND result_run_id IS NULL`

// CompleteDraftCollection saves already-verified drafts and their inbox result
// together. Source authorization and generation happen outside this SQL fence.
func (s *TriggerInboxStore) CompleteDraftCollection(ctx context.Context, lease TriggerLease, runID int64, scope draftRunScope, drafts []taskDraft, requestKey, fingerprint string) (int64, error) {
	if err := validateTriggerLeaseInput(ctx, lease); err != nil {
		return 0, err
	}
	if runID <= 0 || scope.TeamID <= 0 || scope.GroupID <= 0 || scope.InitiatorID <= 0 ||
		requestKey != (model.AgentTriggerOutbox{MessageID: lease.MessageID}).RequestKey() || !validDraftFingerprint(fingerprint) {
		return 0, status.Error(codes.InvalidArgument, "invalid trigger draft collection")
	}
	items, err := prepareWaitingDraftCollection(scope, drafts)
	if err != nil {
		return 0, err
	}
	err = s.triggerLeaseTransaction(ctx, func(ctx context.Context, tx *gorm.DB) error {
		saved, err := lockTriggerLease(ctx, tx, lease)
		if err != nil {
			return err
		}
		if saved.ModelStarted != 1 || saved.ModelAttempts < 1 {
			return ErrInvalidTriggerState
		}
		if err := insertWaitingDraftCollection(tx, runID, scope, items, requestKey, fingerprint); err != nil {
			return err
		}
		// Check DB time again before transitioning: checking live after this
		// UPDATE would reject the completed state and roll back a valid result.
		live, err := lockTriggerLease(ctx, tx, lease)
		if err != nil {
			return err
		}
		if live.ModelStarted != 1 || live.ModelAttempts != saved.ModelAttempts {
			return ErrInvalidTriggerState
		}
		return triggerLeaseAffectedRows(tx.Exec(completeTriggerDraftCollection, runID, lease.MessageID, lease.Token, saved.ModelAttempts))
	})
	if err != nil {
		return 0, err
	}
	return runID, nil
}
