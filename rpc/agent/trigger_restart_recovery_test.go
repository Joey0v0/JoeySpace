package agent

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// This observer preserves every production result. Its notification happens
// after a real empty Claim transaction commits, allowing cancellation without
// a timing-based poll or another SQL query.
type restartRecoveryStore struct {
	*TriggerInboxStore
	empty chan struct{}
}

func (s *restartRecoveryStore) Claim(ctx context.Context) (*TriggerLease, error) {
	lease, err := s.TriggerInboxStore.Claim(ctx)
	if lease == nil && err == nil {
		select {
		case s.empty <- struct{}{}:
		default:
		}
	}
	return lease, err
}

type restartRecoveryRun struct {
	cancel context.CancelFunc
	done   chan error
	exited chan struct{}
}

func startRestartRecoveryWorker(t *testing.T, worker *TriggerWorker) *restartRecoveryRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &restartRecoveryRun{cancel: cancel, done: make(chan error, 1), exited: make(chan struct{})}
	go func() { defer close(run.exited); run.done <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.exited:
		case <-time.After(time.Second):
			t.Error("recovery worker did not finish cleanup")
		}
	})
	return run
}

func waitRestartRecoverySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("recovery stage did not finish")
	}
}

func waitRestartRecoveryLease(t *testing.T, leases <-chan TriggerLease) TriggerLease {
	t.Helper()
	select {
	case lease := <-leases:
		return lease
	case <-time.After(time.Second):
		t.Fatal("processor never received a recovered lease")
		return TriggerLease{}
	}
}

func expectRestartRecoveryClaim(mock sqlmock.Sqlmock, before triggerExecutionRow) *triggerExecutionRow {
	after := claimedTriggerFixture(before)
	rows := sqlmock.NewRows(executionFixtureColumns)
	captured := ""
	expectTriggerClaimCandidate(mock, executionFixtureRows(before))
	mock.ExpectExec(regexp.QuoteMeta(claimTriggerLeaseSQL)).WithArgs(triggerClaimTokenCapture{capture: func(token string) {
		if captured == "" {
			captured = token
			after.Token = sql.NullString{String: token, Valid: true}
			rows.AddRow(triggerClaimValues(after)...)
		}
	}}, before.Event.MessageID, before.Status, before.ModelAttempts, before.ModelStarted, before.Token.String).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(before.Event.MessageID, sqlmock.AnyArg()).WillReturnRows(rows)
	mock.ExpectCommit()
	return &after
}

func restartRecoveryWorker(t *testing.T, store TriggerExecutionStore, processor workerTestProcessor) *TriggerWorker {
	t.Helper()
	worker, err := NewTriggerWorker(store, processor)
	if err != nil {
		t.Fatal(err)
	}
	// This scenario controls stop/restart and database eligibility, not ticker
	// behavior (covered separately). No real sleep advances a lease or retry.
	worker.pollInterval = time.Hour
	worker.renewInterval = time.Hour
	worker.cancelWait = time.Second
	return worker
}

// Runs the real worker and inbox transactions together. SQL replaces MySQL's
// stored rows and clock predicates; this is not an OS crash or DB clock test.
func TestTriggerRestartRecoveryWorkerKeepsChargedBudgetAndFencesOldOwner(t *testing.T) {
	for _, query := range []string{selectClaimableTriggerLease, claimTriggerLeaseSQL} {
		if !strings.Contains(query, "status = 'running'") || !strings.Contains(query, "lease_until <= UTC_TIMESTAMP(6)") {
			t.Fatal("recovery must require a database-expired running lease")
		}
	}
	if !strings.Contains(selectLiveTriggerLease, "lease_token = ? AND lease_until > UTC_TIMESTAMP(6)") {
		t.Fatal("late owner writes must require the current token and DB deadline")
	}
	drafts, mock := testDraftStore(t)
	queued := triggerClaimFixture(false, 0)
	queued.RetryFailures = 1
	queued.RetryAfter = sql.NullTime{Time: queued.ReceivedAt.Add(30 * time.Second), Valid: true}
	first := expectRestartRecoveryClaim(mock, queued)
	// Dynamic rows share the crypto token captured during Claim's SQL update.
	beginRows := sqlmock.NewRows(executionFixtureColumns)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(queued.Event.MessageID, sqlmock.AnyArg()).WillReturnRows(beginRows)
	mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(queued.Event.MessageID, sqlmock.AnyArg(), TriggerModelAttemptLimit).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	firstStore := NewTriggerInboxStore(drafts.db)
	charged := make(chan TriggerLease, 1)
	workerA := restartRecoveryWorker(t, firstStore, func(ctx context.Context, lease TriggerLease) error {
		// Claim has committed by the time Process starts; populate the lock's
		// saved state before BeginModel invokes it, independently of observations.
		beginRows.AddRow(triggerClaimValues(*first)...)
		granted, err := firstStore.BeginModel(ctx, lease)
		if err != nil {
			return err
		}
		if !granted {
			return errors.New("first recovered worker did not gain model permission")
		}
		charged <- lease
		<-ctx.Done()
		return ctx.Err()
	})
	runA := startRestartRecoveryWorker(t, workerA)
	leaseA := waitRestartRecoveryLease(t, charged)
	runA.cancel()
	if err := waitTestWorker(t, runA.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("first worker shutdown=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("shutdown released or changed the charged lease: %v", err)
	}
	saved := *first
	saved.ModelAttempts, saved.ModelStarted = 1, 1
	if leaseA.ModelAttempts != 0 || leaseA.ModelStarted || saved.RetryFailures != 1 {
		t.Fatal("worker observations were used instead of committed budget")
	}

	// Reconstruct both store and worker while SQL still excludes the live owner.
	secondStore := &restartRecoveryStore{TriggerInboxStore: NewTriggerInboxStore(drafts.db), empty: make(chan struct{}, 1)}
	claimed := make(chan TriggerLease, 1)
	proceed := make(chan struct{})
	var processCalls, modelCalls atomic.Int32
	workerB := restartRecoveryWorker(t, secondStore, func(ctx context.Context, lease TriggerLease) error {
		processCalls.Add(1)
		claimed <- lease
		select {
		case <-proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
		granted, err := secondStore.BeginModel(ctx, lease)
		if err != nil {
			return err
		}
		if !granted {
			return errors.New("second recovered worker did not gain model permission")
		}
		modelCalls.Add(1)
		return errors.New("controlled second model failure")
	})
	expectTriggerClaimCandidate(mock, sqlmock.NewRows(executionFixtureColumns))
	mock.ExpectCommit()
	beforeExpiry := startRestartRecoveryWorker(t, workerB)
	waitRestartRecoverySignal(t, secondStore.empty)
	beforeExpiry.cancel()
	if err := waitTestWorker(t, beforeExpiry.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("before-expiry worker=%v", err)
	}
	if processCalls.Load() != 0 || modelCalls.Load() != 0 {
		t.Fatal("worker processed a source before DB lease expiry")
	}

	// The next SQL result represents the same persisted row after DB expiry.
	second := expectRestartRecoveryClaim(mock, saved)
	runB := startRestartRecoveryWorker(t, workerB)
	leaseB := waitRestartRecoveryLease(t, claimed)
	if leaseB.MessageID != leaseA.MessageID || leaseB.Token == leaseA.Token || leaseB.ModelAttempts != 1 || leaseB.ModelStarted || second.RetryFailures != saved.RetryFailures || second.RetryAfter.Valid {
		t.Fatalf("recovery reset budget/count or reused old token: first=%+v second=%+v", leaseA, leaseB)
	}
	// With B paused outside SQL, late operations from A use the real store and
	// must fail at the live-token fence before any budget, run or item UPDATE.
	for _, operation := range []string{"begin", "release", "complete"} {
		mock.ExpectBegin()
		expectTriggerClaimLive(mock, leaseA.MessageID, leaseA.Token, sqlmock.NewRows(executionFixtureColumns))
		mock.ExpectRollback()
		var err error
		switch operation {
		case "begin":
			var granted bool
			granted, err = NewTriggerInboxStore(drafts.db).BeginModel(context.Background(), leaseA)
			if granted {
				t.Fatal("old owner gained another model call")
			}
		case "release":
			err = NewTriggerInboxStore(drafts.db).Release(context.Background(), leaseA)
		case "complete":
			var id int64
			id, err = NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), leaseA, 9001, triggerResultScope(), []taskDraft{collectionStoreDraft()}, triggerNotificationKey(saved.Event), testDraftFingerprint)
			if id != 0 {
				t.Fatal("old owner saved a draft result")
			}
		}
		if !errors.Is(err, ErrTriggerLeaseLost) {
			t.Fatalf("old owner %s returned %v", operation, err)
		}
	}
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, leaseB.MessageID, leaseB.Token, executionFixtureRows(*second))
	mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(leaseB.MessageID, leaseB.Token, TriggerModelAttemptLimit).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	chargedSecond := *second
	chargedSecond.ModelAttempts, chargedSecond.ModelStarted = 2, 1
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, leaseB.MessageID, leaseB.Token, executionFixtureRows(chargedSecond))
	mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxExhausted, TriggerInboxExhausted, 0, saved.RetryFailures, leaseB.MessageID, leaseB.Token, saved.RetryFailures, 2).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectTriggerClaimCandidate(mock, sqlmock.NewRows(executionFixtureColumns))
	mock.ExpectCommit()
	close(proceed)
	waitRestartRecoverySignal(t, secondStore.empty)
	runB.cancel()
	if err := waitTestWorker(t, runB.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("restarted worker shutdown=%v", err)
	}
	if processCalls.Load() != 1 || modelCalls.Load() != 1 {
		t.Fatalf("recovery processing=%d new model attempts=%d", processCalls.Load(), modelCalls.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("worker failed to retire second attempt exactly once: %v", err)
	}
}
