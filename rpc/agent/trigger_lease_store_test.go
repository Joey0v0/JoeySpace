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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

var executionFixtureColumns = []string{"message_id", "action", "event_version", "status", "received_at", "lease_token", "lease_until", "model_attempts", "model_started", "result_run_id", "retry_after", "retry_failures"}

func executionFixtureRow() triggerExecutionRow {
	return triggerExecutionRow{Event: triggerInboxTestEvent(), Status: TriggerInboxRunning,
		ReceivedAt: time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC),
		Token:      sql.NullString{String: strings.Repeat("a", 64), Valid: true},
		Until:      sql.NullTime{Time: time.Date(2026, 10, 4, 2, 0, 30, 0, time.UTC), Valid: true}, ModelAttempts: 1, ModelStarted: 1}
}

func executionFixtureRows(row triggerExecutionRow) *sqlmock.Rows {
	return sqlmock.NewRows(executionFixtureColumns).AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, row.Status, row.ReceivedAt, row.Token, row.Until, row.ModelAttempts, row.ModelStarted, row.ResultRunID, row.RetryAfter, row.RetryFailures)
}

func TestTriggerExecutionStateRejectsInvalidCombinations(t *testing.T) {
	cases := map[string]func(*triggerExecutionRow){
		"bad source":                 func(r *triggerExecutionRow) { r.Event.MessageID = 0 },
		"bad action":                 func(r *triggerExecutionRow) { r.Event.Action = "other" },
		"bad version":                func(r *triggerExecutionRow) { r.Event.Version++ },
		"unknown status":             func(r *triggerExecutionRow) { r.Status = "succeeded" },
		"missing receipt":            func(r *triggerExecutionRow) { r.ReceivedAt = time.Time{} },
		"negative budget":            func(r *triggerExecutionRow) { r.ModelAttempts = -1 },
		"third attempt":              func(r *triggerExecutionRow) { r.ModelAttempts = 3 },
		"bad started flag":           func(r *triggerExecutionRow) { r.ModelStarted = 2 },
		"started without charge":     func(r *triggerExecutionRow) { r.ModelAttempts = 0 },
		"missing token":              func(r *triggerExecutionRow) { r.Token.Valid = false },
		"short token":                func(r *triggerExecutionRow) { r.Token.String = "a" },
		"uppercase token":            func(r *triggerExecutionRow) { r.Token.String = strings.Repeat("A", 64) },
		"missing expiry":             func(r *triggerExecutionRow) { r.Until.Valid = false },
		"zero expiry":                func(r *triggerExecutionRow) { r.Until.Time = time.Time{} },
		"queued with owner":          func(r *triggerExecutionRow) { r.Status = TriggerInboxQueued },
		"exhausted with owner":       func(r *triggerExecutionRow) { r.Status = TriggerInboxExhausted; r.ModelAttempts = 2 },
		"budget spent without begun": func(r *triggerExecutionRow) { r.ModelAttempts = 2; r.ModelStarted = 0 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			row := executionFixtureRow()
			change(&row)
			if !errors.Is(validateTriggerExecutionRow(&row), ErrInvalidTriggerState) {
				t.Fatal("invalid execution state accepted")
			}
		})
	}
	if !errors.Is(validateTriggerExecutionRow(nil), ErrInvalidTriggerState) {
		t.Fatal("nil state accepted")
	}
}

func TestTriggerExecutionStateAllowsOnlyReachableBudgetStates(t *testing.T) {
	for _, state := range []string{TriggerInboxQueued, TriggerInboxRunning, TriggerInboxExhausted} {
		for attempts := 0; attempts <= 2; attempts++ {
			for started := 0; started <= 1; started++ {
				row := executionFixtureRow()
				row.Status, row.ModelAttempts, row.ModelStarted = state, attempts, started
				if state != TriggerInboxRunning {
					row.Token, row.Until = sql.NullString{}, sql.NullTime{}
				}
				want := state == TriggerInboxQueued && attempts < 2 && started == 0 ||
					state == TriggerInboxExhausted && attempts == 2 && started == 0 ||
					state == TriggerInboxRunning && (started == 0 && attempts < 2 || started == 1 && attempts > 0)
				if got := validateTriggerExecutionRow(&row) == nil; got != want {
					t.Fatalf("state=%s attempts=%d started=%d got=%v want=%v", state, attempts, started, got, want)
				}
			}
		}
	}
}

func TestTriggerExecutionRowReadRejectsNullDuplicateAndLateIteratorFailure(t *testing.T) {
	for _, name := range []string{"NULL budget", "NULL started", "duplicate", "iterator failure"} {
		t.Run(name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			rows := executionFixtureRows(row)
			switch name {
			case "NULL budget":
				rows = sqlmock.NewRows(executionFixtureColumns).AddRow(row.Event.MessageID, row.Event.Action, 1, row.Status, row.ReceivedAt, row.Token, row.Until, nil, 1, nil, nil, 0)
			case "NULL started":
				rows = sqlmock.NewRows(executionFixtureColumns).AddRow(row.Event.MessageID, row.Event.Action, 1, row.Status, row.ReceivedAt, row.Token, row.Until, 1, nil, nil, nil, 0)
			case "duplicate":
				rows.AddRow(row.Event.MessageID, row.Event.Action, 1, row.Status, row.ReceivedAt, row.Token, row.Until, 1, 1, nil, nil, 0)
			case "iterator failure":
				rows.AddRow(row.Event.MessageID, row.Event.Action, 1, row.Status, row.ReceivedAt, row.Token, row.Until, 1, 1, nil, nil, 0).RowError(1, errors.New("private driver detail"))
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(rows)
			got, err := readTriggerExecutionRow(context.Background(), drafts.db, selectLiveTriggerLease, row.Event.MessageID, row.Token.String)
			if got != nil || err == nil {
				t.Fatalf("bad rows leaked lease: %#v %v", got, err)
			}
		})
	}
}

func TestTriggerLeaseFenceRejectsLostOwnerBeforeCallback(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	lease := *row.lease()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnRows(sqlmock.NewRows(executionFixtureColumns))
	mock.ExpectRollback()
	called := false
	err := NewTriggerInboxStore(drafts.db).withTriggerLease(context.Background(), lease, func(context.Context, *gorm.DB) error { called = true; return nil })
	if !errors.Is(err, ErrTriggerLeaseLost) || called {
		t.Fatalf("lost owner ran callback: called=%v err=%v", called, err)
	}
}

func TestTriggerLeaseFenceRechecksDBTimeAndRollsBackWrites(t *testing.T) {
	for _, validAtEnd := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired during SQL", true: "still live"}[validAtEnd], func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			lease := *row.lease()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnRows(executionFixtureRows(row))
			mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WithArgs(int64(9001), int64(200), int64(300), int64(400), "agent-trigger:tasks:9007199254740993", testDraftFingerprint, string(draftWaitingConfirmation), "collection", 1).WillReturnResult(sqlmock.NewResult(0, 1))
			endRows := sqlmock.NewRows(executionFixtureColumns)
			if validAtEnd {
				endRows = executionFixtureRows(row)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnRows(endRows)
			if validAtEnd {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := NewTriggerInboxStore(drafts.db).withTriggerLease(context.Background(), lease, func(ctx context.Context, tx *gorm.DB) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > triggerInboxSQLTimeout {
					t.Error("missing bounded SQL context")
				}
				return tx.Exec(insertDraftCollectionRun, int64(9001), int64(200), int64(300), int64(400), "agent-trigger:tasks:9007199254740993", testDraftFingerprint, string(draftWaitingConfirmation), "collection", 1).Error
			})
			if validAtEnd && err != nil || !validAtEnd && !errors.Is(err, ErrTriggerLeaseLost) {
				t.Fatalf("fence commit decision: %v", err)
			}
		})
	}
}

func TestTriggerLeaseFenceUncertainCommitAndCallbackFailureAreSafe(t *testing.T) {
	for _, failure := range []string{"callback", "commit"} {
		t.Run(failure, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(executionFixtureRows(row))
			if failure == "commit" {
				mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(executionFixtureRows(row))
				mock.ExpectCommit().WillReturnError(errors.New("private uncertain commit"))
			} else {
				mock.ExpectRollback()
			}
			err := NewTriggerInboxStore(drafts.db).withTriggerLease(context.Background(), *row.lease(), func(context.Context, *gorm.DB) error {
				if failure == "callback" {
					return errors.New("private callback SQL detail")
				}
				return nil
			})
			if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), row.Token.String) {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

func TestTriggerLeaseFenceRejectsBadInputAndPriorCancellationWithoutSQL(t *testing.T) {
	drafts, _ := testDraftStore(t)
	store := NewTriggerInboxStore(drafts.db)
	row := executionFixtureRow()
	callback := func(context.Context, *gorm.DB) error { t.Error("invalid input reached mutation"); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range []struct {
		ctx   context.Context
		lease TriggerLease
		want  codes.Code
	}{
		{nil, *row.lease(), codes.InvalidArgument}, {ctx, *row.lease(), codes.Canceled},
		{context.Background(), TriggerLease{MessageID: row.Event.MessageID, Token: "bad"}, codes.InvalidArgument},
		{context.Background(), TriggerLease{MessageID: 0, Token: row.Token.String}, codes.InvalidArgument},
	} {
		if err := store.withTriggerLease(check.ctx, check.lease, callback); status.Code(err) != check.want {
			t.Fatalf("input rejection: %v", err)
		}
	}
	if err := store.withTriggerLease(context.Background(), *row.lease(), nil); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
