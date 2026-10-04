package agent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTriggerRetryBackoffInitialDoublingAndSaturatedBounds(t *testing.T) {
	for _, tc := range []struct{ failures, seconds, next int }{{0, 30, 1}, {1, 60, 2}, {2, 120, 3}, {3, 240, 4}, {4, 480, 5}, {5, 960, 6}, {6, 1920, 7}, {7, 3600, 8}, {8, 3600, 8}} {
		seconds, next := triggerRetryBackoff(tc.failures)
		if seconds != tc.seconds || next != tc.next {
			t.Fatalf("failures=%d seconds=%d next=%d", tc.failures, seconds, next)
		}
	}
}

func TestTriggerRetryReleasePersistsOnlySavedCountAndIndependentModelBudget(t *testing.T) {
	for _, tc := range []struct{ failures, seconds, next int }{{0, 30, 1}, {1, 60, 2}, {6, 1920, 7}, {7, 3600, 8}, {8, 3600, 8}} {
		for _, attempts := range []int{0, 1} {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			row.RetryFailures = tc.failures
			row.ModelAttempts = attempts
			row.ModelStarted = 0
			caller := *row.lease()
			caller.ModelAttempts = 999
			caller.ModelStarted = true
			caller.Until = time.Time{}
			mock.ExpectBegin()
			expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, executionFixtureRows(row))
			mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxQueued, TriggerInboxQueued, tc.seconds, tc.next, row.Event.MessageID, row.Token.String, tc.failures, attempts).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			if err := NewTriggerInboxStore(drafts.db).Release(context.Background(), caller); err != nil {
				t.Fatalf("failures=%d budget=%d err=%v", tc.failures, attempts, err)
			}
		}
	}
	if !strings.Contains(releaseTriggerLease, "DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? SECOND)") || strings.Contains(releaseTriggerLease, "model_attempts = 0") {
		t.Fatal("retry must use database time without resetting model attempts")
	}
}

func TestTriggerRetryExhaustedReleasePreservesFailureCountAndHasNoWait(t *testing.T) {
	for _, failures := range []int{0, 1, 8} {
		drafts, mock := testDraftStore(t)
		row := executionFixtureRow()
		row.ModelAttempts = 2
		row.RetryFailures = failures
		mock.ExpectBegin()
		expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, executionFixtureRows(row))
		mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxExhausted, TriggerInboxExhausted, 0, failures, row.Event.MessageID, row.Token.String, failures, 2).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := NewTriggerInboxStore(drafts.db).Release(context.Background(), *row.lease()); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(retireExpiredTriggerLeaseSQL, "retry_after = NULL") || strings.Contains(retireExpiredTriggerLeaseSQL, "retry_failures =") {
		t.Fatal("expired final attempt must retire without changing failure count")
	}
}

func TestTriggerRetryWaitingSourceDoesNotBlockEligibleLaterSource(t *testing.T) {
	// SQL, not the process wall clock, decides which row is eligible. A waiting
	// smaller message ID is absent from the result and cannot block this row.
	drafts, mock := testDraftStore(t)
	before := triggerClaimFixture(false, 0)
	before.Event.MessageID++
	after := claimedTriggerFixture(before)
	expectTriggerClaimCandidate(mock, triggerClaimRows(before))
	expectTriggerClaimUpdate(mock, before, after, 1)
	expectTriggerClaimLive(mock, before.Event.MessageID, after.Token.String, triggerClaimRows(after))
	mock.ExpectCommit()
	lease, err := NewTriggerInboxStore(drafts.db).claimWithToken(context.Background(), func() (string, error) { return after.Token.String, nil })
	if err != nil || lease == nil || lease.MessageID != before.Event.MessageID {
		t.Fatalf("later eligible source=%+v err=%v", lease, err)
	}
	expectTriggerClaimCandidate(mock, triggerClaimRows())
	mock.ExpectCommit()
	if lease, err := NewTriggerInboxStore(drafts.db).Claim(context.Background()); lease != nil || err != nil {
		t.Fatalf("waiting source was polled early: %+v %v", lease, err)
	}
	for _, query := range []string{selectClaimableTriggerLease, claimTriggerLeaseSQL} {
		if !strings.Contains(query, "retry_after IS NULL OR retry_after <= UTC_TIMESTAMP(6)") {
			t.Fatal("missing database-clock retry predicate")
		}
	}
	if !strings.Contains(selectClaimableTriggerLease, "ORDER BY message_id LIMIT 1 FOR UPDATE SKIP LOCKED") || !strings.Contains(claimTriggerLeaseSQL, "retry_after = NULL") || strings.Contains(claimTriggerLeaseSQL, "retry_failures =") {
		t.Fatal("claim fairness or persistent retry count changed")
	}
}

func TestTriggerRetryProcessReconstructionKeepsWaitCountAndGrantsOnlyModelBudget(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	row.ModelAttempts, row.ModelStarted = 0, 0
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, executionFixtureRows(row))
	mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxQueued, TriggerInboxQueued, 30, 1, row.Event.MessageID, row.Token.String, 0, 0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := NewTriggerInboxStore(drafts.db).Release(context.Background(), *row.lease()); err != nil {
		t.Fatal(err)
	}
	queued := row
	queued.Status = TriggerInboxQueued
	queued.Token = sql.NullString{}
	queued.Until = sql.NullTime{}
	queued.RetryFailures = 1
	queued.RetryAfter = sql.NullTime{Time: row.ReceivedAt.Add(30 * time.Second), Valid: true}
	// Reconstruct the store while the database still excludes the waiting row.
	expectTriggerClaimCandidate(mock, triggerClaimRows())
	mock.ExpectCommit()
	if lease, err := NewTriggerInboxStore(drafts.db).Claim(context.Background()); lease != nil || err != nil {
		t.Fatalf("early claim=%+v %v", lease, err)
	}
	// Once the database declares it due, Claim clears only retry_after.
	live := claimedTriggerFixture(queued)
	expectTriggerClaimCandidate(mock, triggerClaimRows(queued))
	expectTriggerClaimUpdate(mock, queued, live, 1)
	expectTriggerClaimLive(mock, live.Event.MessageID, live.Token.String, triggerClaimRows(live))
	mock.ExpectCommit()
	lease, err := NewTriggerInboxStore(drafts.db).claimWithToken(context.Background(), func() (string, error) { return live.Token.String, nil })
	if err != nil || lease == nil || lease.ModelAttempts != 0 {
		t.Fatalf("reconstructed lease=%+v %v", lease, err)
	}
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, live.Event.MessageID, live.Token.String, triggerClaimRows(live))
	mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(live.Event.MessageID, live.Token.String, TriggerModelAttemptLimit).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if granted, err := NewTriggerInboxStore(drafts.db).BeginModel(context.Background(), *lease); !granted || err != nil {
		t.Fatalf("retry spent model budget early: %v %v", granted, err)
	}
	live.ModelAttempts, live.ModelStarted = 1, 1
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, live.Event.MessageID, live.Token.String, triggerClaimRows(live))
	mock.ExpectCommit()
	if granted, err := NewTriggerInboxStore(drafts.db).BeginModel(context.Background(), *lease); granted || err != nil {
		t.Fatalf("same lease granted twice: %v %v", granted, err)
	}
}

func TestTriggerRetryStrictlyRejectsNullMalformedAndUnreachableFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*triggerExecutionRow)
		column int
		value  driver.Value
	}{
		{name: "NULL failure counter", column: 11, value: nil},
		{name: "negative failure counter", column: 11, value: -1},
		{name: "failure counter above cap", column: 11, value: 9},
		{name: "malformed retry date", column: 10, value: "not-a-date"},
		{name: "zero retry date", column: 10, value: time.Time{}},
		{name: "epoch retry date", column: 10, value: time.Unix(0, 0)},
		{name: "outside SQL date range", mutate: func(r *triggerExecutionRow) {
			r.Status = TriggerInboxQueued
			r.Token = sql.NullString{}
			r.Until = sql.NullTime{}
			r.ModelStarted = 0
			r.RetryFailures = 1
		}, column: 10, value: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "running cannot retain wait", column: 10, value: time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "queued count requires wait", mutate: func(r *triggerExecutionRow) {
			r.Status = TriggerInboxQueued
			r.Token = sql.NullString{}
			r.Until = sql.NullTime{}
			r.ModelAttempts = 0
			r.ModelStarted = 0
			r.RetryFailures = 1
		}, column: -1},
		{name: "initial queued cannot have wait", mutate: func(r *triggerExecutionRow) {
			r.Status = TriggerInboxQueued
			r.Token = sql.NullString{}
			r.Until = sql.NullTime{}
			r.ModelAttempts = 0
			r.ModelStarted = 0
			r.RetryAfter = sql.NullTime{Time: r.ReceivedAt, Valid: true}
		}, column: -1},
		{name: "exhausted cannot have wait", mutate: func(r *triggerExecutionRow) {
			r.Status = TriggerInboxExhausted
			r.Token = sql.NullString{}
			r.Until = sql.NullTime{}
			r.ModelAttempts = 2
			r.ModelStarted = 0
			r.RetryFailures = 8
			r.RetryAfter = sql.NullTime{Time: r.ReceivedAt, Valid: true}
		}, column: -1},
		{name: "completed cannot have wait", mutate: func(r *triggerExecutionRow) {
			r.Status = TriggerInboxCompleted
			r.Token = sql.NullString{}
			r.Until = sql.NullTime{}
			r.ModelStarted = 0
			r.ResultRunID = sql.NullInt64{Int64: 9001, Valid: true}
			r.RetryFailures = 1
			r.RetryAfter = sql.NullTime{Time: r.ReceivedAt, Valid: true}
		}, column: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			if tc.mutate != nil {
				tc.mutate(&row)
			}
			values := triggerClaimValues(row)
			if tc.column >= 0 {
				values[tc.column] = tc.value
			}
			expectTriggerClaimCandidate(mock, sqlmock.NewRows(triggerClaimColumns).AddRow(values...))
			mock.ExpectRollback()
			if lease, err := NewTriggerInboxStore(drafts.db).Claim(context.Background()); lease != nil || !errors.Is(err, ErrInvalidTriggerState) {
				t.Fatalf("bad retry fields allowed claim: %+v %v", lease, err)
			}
		})
	}
	for _, failures := range []int{0, 1, 8} {
		row := executionFixtureRow()
		row.RetryFailures = failures
		if err := validateTriggerExecutionRow(&row); err != nil {
			t.Fatalf("valid running count=%d err=%v", failures, err)
		}
		row.Status = TriggerInboxQueued
		row.Token = sql.NullString{}
		row.Until = sql.NullTime{}
		row.ModelStarted = 0
		if failures > 0 {
			row.RetryAfter = sql.NullTime{Time: row.ReceivedAt, Valid: true}
		}
		if err := validateTriggerExecutionRow(&row); err != nil {
			t.Fatalf("valid queued count=%d err=%v", failures, err)
		}
	}
}

func TestTriggerRetryClaimRejectsCountResetDuringLiveRead(t *testing.T) {
	drafts, mock := testDraftStore(t)
	before := triggerClaimFixture(false, 0)
	before.RetryFailures = 8
	before.RetryAfter = sql.NullTime{Time: before.ReceivedAt, Valid: true}
	after := claimedTriggerFixture(before)
	after.RetryFailures = 0
	expectTriggerClaimCandidate(mock, triggerClaimRows(before))
	expectTriggerClaimUpdate(mock, before, after, 1)
	expectTriggerClaimLive(mock, before.Event.MessageID, after.Token.String, triggerClaimRows(after))
	mock.ExpectRollback()
	if lease, err := NewTriggerInboxStore(drafts.db).claimWithToken(context.Background(), func() (string, error) { return after.Token.String, nil }); lease != nil || !errors.Is(err, ErrInvalidTriggerState) {
		t.Fatalf("count reset accepted: %+v %v", lease, err)
	}
}

func TestTriggerRetryRenewKeepsFailureAndModelCounters(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	row.RetryFailures = 8
	renewed := row
	renewed.Until.Time = renewed.Until.Time.Add(30 * time.Second)
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, executionFixtureRows(row))
	mock.ExpectExec(regexp.QuoteMeta(renewTriggerLeaseSQL)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnResult(sqlmock.NewResult(0, 1))
	expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, executionFixtureRows(renewed))
	mock.ExpectCommit()
	if lease, err := NewTriggerInboxStore(drafts.db).Renew(context.Background(), *row.lease()); err != nil || lease == nil || lease.ModelAttempts != row.ModelAttempts || !lease.ModelStarted {
		t.Fatalf("renew=%+v %v", lease, err)
	}
}

func TestTriggerRetryUncertainReleaseDoesNotReplayOldOwnerOrResetPersistentWait(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	row.ModelAttempts, row.ModelStarted = 0, 0
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, executionFixtureRows(row))
	mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxQueued, TriggerInboxQueued, 30, 1, row.Event.MessageID, row.Token.String, 0, 0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("private uncertain retry commit"))
	store := NewTriggerInboxStore(drafts.db)
	if err := store.Release(context.Background(), *row.lease()); status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	// If the commit reached MySQL, the old token cannot release or increment again.
	mock.ExpectBegin()
	expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, sqlmock.NewRows(executionFixtureColumns))
	mock.ExpectRollback()
	if err := NewTriggerInboxStore(drafts.db).Release(context.Background(), *row.lease()); !errors.Is(err, ErrTriggerLeaseLost) {
		t.Fatal(err)
	}
	event := row.Event
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, event.Version, TriggerInboxQueued).WillReturnError(&mysql.MySQLError{Number: 1062})
	mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).WillReturnRows(triggerInboxRows(event, TriggerInboxQueued, row.ReceivedAt))
	mock.ExpectCommit()
	if err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	// No UPDATE was permitted for the Kafka replay, and DB still excludes the wait.
	expectTriggerClaimCandidate(mock, triggerClaimRows())
	mock.ExpectCommit()
	if lease, err := NewTriggerInboxStore(drafts.db).Claim(context.Background()); lease != nil || err != nil {
		t.Fatalf("retry replay lost wait: %+v %v", lease, err)
	}
}
