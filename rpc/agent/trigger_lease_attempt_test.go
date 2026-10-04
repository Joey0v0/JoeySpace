package agent

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func triggerAttemptTestLease() TriggerLease {
	return TriggerLease{MessageID: 9007199254740993, Token: strings.Repeat("a", 64), Until: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func triggerAttemptTestValues(lease TriggerLease, attempts, started int) []driver.Value {
	return []driver.Value{lease.MessageID, model.AgentTriggerAction, model.AgentTriggerVersion, TriggerInboxRunning,
		time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC), lease.Token, lease.Until, attempts, started}
}

func triggerAttemptTestRows(values ...driver.Value) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"message_id", "action", "event_version", "status", "received_at",
		"lease_token", "lease_until", "model_attempts", "model_started"})
	if len(values) != 0 {
		rows.AddRow(values...)
	}
	return rows
}

func expectTriggerAttemptLock(mock sqlmock.Sqlmock, lease TriggerLease, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).
		WillReturnRows(rows).RowsWillBeClosed()
}

func callTriggerAttemptOperation(s *TriggerInboxStore, operation string, ctx context.Context, lease TriggerLease) (bool, error) {
	if operation == "begin" {
		return s.BeginModel(ctx, lease)
	}
	return false, s.Release(ctx, lease)
}

func TestTriggerBeginModelGrantsFirstAndSecondBudgetFromStoredFacts(t *testing.T) {
	for _, attempts := range []int{0, 1} {
		t.Run(map[int]string{0: "first", 1: "second"}[attempts], func(t *testing.T) {
			draft, mock := testDraftStore(t)
			s := NewTriggerInboxStore(draft.db)
			saved := triggerAttemptTestLease()
			caller := saved
			caller.Until = time.Time{}
			caller.ModelAttempts = 999
			caller.ModelStarted = true
			mock.ExpectBegin()
			expectTriggerAttemptLock(mock, caller, triggerAttemptTestRows(triggerAttemptTestValues(saved, attempts, 0)...))
			mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(saved.MessageID, saved.Token, TriggerModelAttemptLimit).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			granted, err := s.BeginModel(context.Background(), caller)
			if err != nil || !granted {
				t.Fatalf("stored budget %d: granted=%t err=%v", attempts, granted, err)
			}
		})
	}
}

func TestTriggerBeginModelReplayNeverGrantsAnotherCall(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		draft, mock := testDraftStore(t)
		s := NewTriggerInboxStore(draft.db)
		lease := triggerAttemptTestLease()
		lease.ModelAttempts, lease.ModelStarted = 0, false
		mock.ExpectBegin()
		expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(triggerAttemptTestValues(lease, attempts, 1)...))
		mock.ExpectCommit() // No UPDATE, even when caller claims it never started.
		if granted, err := s.BeginModel(context.Background(), lease); err != nil || granted {
			t.Fatalf("replay budget %d: granted=%t err=%v", attempts, granted, err)
		}
	}
}

func TestTriggerBeginModelUncertainCommitIsFalseAndReplayStaysSpent(t *testing.T) {
	draft, mock := testDraftStore(t)
	s := NewTriggerInboxStore(draft.db)
	lease := triggerAttemptTestLease()
	mock.ExpectBegin()
	expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(triggerAttemptTestValues(lease, 0, 0)...))
	mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(lease.MessageID, lease.Token, TriggerModelAttemptLimit).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("secret uncertain database commit"))
	granted, err := s.BeginModel(context.Background(), lease)
	if granted || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "secret") {
		t.Fatalf("uncertain commit granted model permission: granted=%t err=%v", granted, err)
	}
	// The commit may have reached MySQL. A same-token replay cannot grant again.
	mock.ExpectBegin()
	expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(triggerAttemptTestValues(lease, 1, 1)...))
	mock.ExpectCommit()
	if granted, err := s.BeginModel(context.Background(), lease); granted || err != nil {
		t.Fatalf("uncertain budget was reused: granted=%t err=%v", granted, err)
	}
}

func TestTriggerReleaseUsesSavedBudgetAndDoesNotResetAttempts(t *testing.T) {
	for _, tc := range []struct {
		name              string
		attempts, started int
		next              string
	}{
		{"before any model", 0, 0, TriggerInboxQueued},
		{"second lease before model", 1, 0, TriggerInboxQueued},
		{"first model failed", 1, 1, TriggerInboxQueued},
		{"second model failed", 2, 1, TriggerInboxExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft, mock := testDraftStore(t)
			s := NewTriggerInboxStore(draft.db)
			saved := triggerAttemptTestLease()
			caller := saved
			caller.Until = time.Time{}
			caller.ModelAttempts, caller.ModelStarted = 999, false
			mock.ExpectBegin()
			expectTriggerAttemptLock(mock, caller, triggerAttemptTestRows(triggerAttemptTestValues(saved, tc.attempts, tc.started)...))
			mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(tc.next, saved.MessageID, saved.Token).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			if err := s.Release(context.Background(), caller); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTriggerAttemptLostLeaseAndInvalidAffectedRows(t *testing.T) {
	for _, operation := range []string{"begin", "release"} {
		t.Run(operation, func(t *testing.T) {
			lease := triggerAttemptTestLease()
			draft, mock := testDraftStore(t)
			s := NewTriggerInboxStore(draft.db)
			mock.ExpectBegin()
			expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows())
			mock.ExpectRollback()
			if granted, err := callTriggerAttemptOperation(s, operation, context.Background(), lease); granted || err != ErrTriggerLeaseLost {
				t.Fatalf("old/expired lease: granted=%t err=%v", granted, err)
			}
			for _, affected := range []int64{0, 2, -1} {
				mock.ExpectBegin()
				expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(triggerAttemptTestValues(lease, 0, 0)...))
				if operation == "begin" {
					mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(lease.MessageID, lease.Token, TriggerModelAttemptLimit).
						WillReturnResult(sqlmock.NewResult(0, affected))
				} else {
					mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxQueued, lease.MessageID, lease.Token).
						WillReturnResult(sqlmock.NewResult(0, affected))
				}
				mock.ExpectRollback()
				want := ErrInvalidTriggerState
				if affected == 0 {
					want = ErrTriggerLeaseLost
				}
				if granted, err := callTriggerAttemptOperation(s, operation, context.Background(), lease); granted || err != want {
					t.Fatalf("affected=%d granted=%t err=%v", affected, granted, err)
				}
			}
		})
	}
}

func TestTriggerAttemptInvalidSavedRowsNeverMutate(t *testing.T) {
	for _, operation := range []string{"begin", "release"} {
		for _, tc := range []struct {
			name   string
			column int
			value  driver.Value
		}{
			{"null id", 0, nil}, {"wrong source", 0, int64(43)}, {"wrong action", 1, "other"}, {"wrong version", 2, 2},
			{"unknown status", 3, "succeeded"}, {"queued with owner", 3, TriggerInboxQueued}, {"null receipt", 4, nil},
			{"missing token", 5, nil}, {"different token", 5, strings.Repeat("b", 64)}, {"missing deadline", 6, nil},
			{"null attempts", 7, nil}, {"negative attempts", 7, -1}, {"over budget", 7, 3},
			{"unstarted exhausted budget", 7, 2}, {"started without attempt", 8, 1}, {"invalid started", 8, 2}, {"null started", 8, nil},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				draft, mock := testDraftStore(t)
				s := NewTriggerInboxStore(draft.db)
				lease := triggerAttemptTestLease()
				values := triggerAttemptTestValues(lease, 0, 0)
				values[tc.column] = tc.value
				mock.ExpectBegin()
				expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(values...))
				mock.ExpectRollback()
				if granted, err := callTriggerAttemptOperation(s, operation, context.Background(), lease); granted || err != ErrInvalidTriggerState {
					t.Fatalf("bad state granted=%t err=%v", granted, err)
				}
			})
		}
		t.Run(operation+"/duplicate rows", func(t *testing.T) {
			draft, mock := testDraftStore(t)
			lease := triggerAttemptTestLease()
			values := triggerAttemptTestValues(lease, 0, 0)
			mock.ExpectBegin()
			expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(values...).AddRow(values...))
			mock.ExpectRollback()
			if granted, err := callTriggerAttemptOperation(NewTriggerInboxStore(draft.db), operation, context.Background(), lease); granted || err != ErrInvalidTriggerState {
				t.Fatalf("duplicate state granted=%t err=%v", granted, err)
			}
		})
	}
}

func TestTriggerAttemptDatabaseFailuresAreSafeAndNeverGrant(t *testing.T) {
	for _, operation := range []string{"begin", "release"} {
		for _, stage := range []string{"begin transaction", "select", "update", "commit"} {
			t.Run(operation+"/"+stage, func(t *testing.T) {
				draft, mock := testDraftStore(t)
				lease := triggerAttemptTestLease()
				private := errors.New("secret SQL/password/token")
				if stage == "begin transaction" {
					mock.ExpectBegin().WillReturnError(private)
				} else {
					mock.ExpectBegin()
					if stage == "select" {
						mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnError(private)
						mock.ExpectRollback()
					} else {
						expectTriggerAttemptLock(mock, lease, triggerAttemptTestRows(triggerAttemptTestValues(lease, 0, 0)...))
						query := beginTriggerModel
						if operation == "release" {
							query = releaseTriggerLease
						}
						update := mock.ExpectExec(regexp.QuoteMeta(query))
						if stage == "update" {
							update.WillReturnError(private)
							mock.ExpectRollback()
						} else {
							update.WillReturnResult(sqlmock.NewResult(0, 1))
							mock.ExpectCommit().WillReturnError(private)
						}
					}
				}
				granted, err := callTriggerAttemptOperation(NewTriggerInboxStore(draft.db), operation, context.Background(), lease)
				if granted || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), lease.Token) {
					t.Fatalf("database failure granted=%t err=%v", granted, err)
				}
			})
		}
	}
}

func TestTriggerAttemptInputAndCancellationBeforeSQL(t *testing.T) {
	for _, operation := range []string{"begin", "release"} {
		t.Run(operation, func(t *testing.T) {
			draft, _ := testDraftStore(t) // No SQL expectations: guards must precede DB.
			s := NewTriggerInboxStore(draft.db)
			valid := triggerAttemptTestLease()
			if granted, err := callTriggerAttemptOperation(s, operation, nil, valid); granted || status.Code(err) != codes.InvalidArgument {
				t.Fatalf("nil context: %t %v", granted, err)
			}
			for _, lease := range []TriggerLease{{}, {MessageID: 42, Token: "short"}, {MessageID: 42, Token: strings.Repeat("A", 64)}} {
				if granted, err := callTriggerAttemptOperation(s, operation, context.Background(), lease); granted || status.Code(err) != codes.InvalidArgument {
					t.Fatalf("invalid lease: %t %v", granted, err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if granted, err := callTriggerAttemptOperation(s, operation, ctx, TriggerLease{}); granted || status.Code(err) != codes.Canceled {
				t.Fatalf("cancel priority: %t %v", granted, err)
			}
			var nilStore *TriggerInboxStore
			if granted, err := callTriggerAttemptOperation(nilStore, operation, context.Background(), valid); granted || status.Code(err) != codes.Unavailable {
				t.Fatalf("nil store: %t %v", granted, err)
			}
		})
	}
}

func TestTriggerAttemptSQLDeadlineDoesNotGrantOrMutateAfterCancellation(t *testing.T) {
	for _, operation := range []string{"begin", "release"} {
		t.Run(operation, func(t *testing.T) {
			draft, mock := testDraftStore(t)
			lease := triggerAttemptTestLease()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).
				WillDelayFor(time.Second).WillReturnRows(triggerAttemptTestRows(triggerAttemptTestValues(lease, 0, 0)...))
			mock.ExpectRollback()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if granted, err := callTriggerAttemptOperation(NewTriggerInboxStore(draft.db), operation, ctx, lease); granted || status.Code(err) != codes.DeadlineExceeded {
				t.Fatalf("SQL deadline: granted=%t err=%v", granted, err)
			}
			// database/sql can finish its context-triggered rollback after Return.
			// Wait for that real rollback rather than dropping its expectation.
			until := time.Now().Add(time.Second)
			for {
				err := mock.ExpectationsWereMet()
				if err == nil {
					break
				}
				if !time.Now().Before(until) {
					t.Fatalf("asynchronous rollback did not complete: %v", err)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
