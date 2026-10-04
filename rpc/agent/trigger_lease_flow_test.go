package agent

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

var _ TriggerExecutionStore = (*TriggerInboxStore)(nil)

// Uses real store transactions with SQL substituted for MySQL. Separate store
// instances represent process reconstruction; no worker/model is wired yet.
func TestTriggerLeaseRecoveryBudgetAndNotificationReplayFlow(t *testing.T) {
	drafts, mock := testDraftStore(t)
	source := executionFixtureRow()
	source.Status, source.Token, source.Until, source.ModelAttempts, source.ModelStarted = TriggerInboxQueued, sql.NullString{}, sql.NullTime{}, 0, 0
	claim := func(before triggerExecutionRow, token string) triggerExecutionRow {
		t.Helper()
		after := before
		after.Status, after.Token, after.Until, after.ModelStarted = TriggerInboxRunning, sql.NullString{String: token, Valid: true}, sql.NullTime{Time: time.Date(2037, 1, 1, 0, 0, 30, 0, time.UTC), Valid: true}, 0
		after.RetryAfter = sql.NullTime{}
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(executionFixtureRows(before))
		mock.ExpectExec(regexp.QuoteMeta(claimTriggerLeaseSQL)).WithArgs(token, before.Event.MessageID, before.Status, before.ModelAttempts, before.ModelStarted, before.Token.String).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(after.Event.MessageID, token).WillReturnRows(executionFixtureRows(after))
		mock.ExpectCommit()
		got, err := NewTriggerInboxStore(drafts.db).claimWithToken(context.Background(), func() (string, error) { return token, nil })
		if err != nil || got == nil || *got != *after.lease() {
			t.Fatalf("recover claim: %#v %v", got, err)
		}
		return after
	}
	begin := func(row triggerExecutionRow, want bool) {
		t.Helper()
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(executionFixtureRows(row))
		if want {
			mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(row.Event.MessageID, row.Token.String, TriggerModelAttemptLimit).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		got, err := NewTriggerInboxStore(drafts.db).BeginModel(context.Background(), *row.lease())
		if err != nil || got != want {
			t.Fatalf("model grant=%v want=%v err=%v", got, want, err)
		}
	}
	release := func(row triggerExecutionRow, next string) {
		t.Helper()
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(executionFixtureRows(row))
		seconds, failures := triggerRetryBackoff(row.RetryFailures)
		if next == TriggerInboxExhausted {
			seconds, failures = 0, row.RetryFailures
		}
		mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(next, next, seconds, failures, row.Event.MessageID, row.Token.String, row.RetryFailures, row.ModelAttempts).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := NewTriggerInboxStore(drafts.db).Release(context.Background(), *row.lease()); err != nil {
			t.Fatal(err)
		}
	}
	lost := func(lease TriggerLease) {
		t.Helper()
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnRows(sqlmock.NewRows(executionFixtureColumns))
		mock.ExpectRollback()
		if granted, err := NewTriggerInboxStore(drafts.db).BeginModel(context.Background(), lease); granted || !errors.Is(err, ErrTriggerLeaseLost) {
			t.Fatalf("stale owner got model permission: %v %v", granted, err)
		}
	}

	// A failed source read can release before model without spending either try.
	preflight := claim(source, strings.Repeat("1", 64))
	release(preflight, TriggerInboxQueued)
	source.RetryFailures = 1
	source.RetryAfter = sql.NullTime{Time: source.ReceivedAt.Add(30 * time.Second), Valid: true}
	first := claim(source, strings.Repeat("2", 64))
	begin(first, true)
	first.ModelAttempts, first.ModelStarted = 1, 1
	begin(first, false) // repeated permission request never authorizes a second call
	// Kafka replays while processing. Real publisher/consumer must not reset it.
	broker := publishedInboxFlowNotification(t)
	expectInboxFlowInsert(mock).WillReturnError(&mysql.MySQLError{Number: 1062})
	mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(first.Event.MessageID).
		WillReturnRows(triggerInboxRows(first.Event, TriggerInboxRunning, first.ReceivedAt))
	mock.ExpectCommit()
	runInboxFlowConsumer(t, NewTriggerInboxStore(drafts.db), broker, mock)

	// A reconstructed process reclaims a DB-expired first attempt with a new token.
	second := claim(first, strings.Repeat("3", 64))
	lost(*first.lease())
	if second.ModelAttempts != 1 || second.ModelStarted != 0 {
		t.Fatal("recovery reset prior model budget")
	}
	begin(second, true)
	second.ModelAttempts, second.ModelStarted = 2, 1
	begin(second, false)
	release(second, TriggerInboxExhausted)
	lost(*second.lease()) // no live lease exists to grant a third call

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(sqlmock.NewRows(executionFixtureColumns))
	mock.ExpectCommit()
	if lease, err := NewTriggerInboxStore(drafts.db).Claim(context.Background()); lease != nil || err != nil {
		t.Fatalf("exhausted source queued again: %#v %v", lease, err)
	}
}
