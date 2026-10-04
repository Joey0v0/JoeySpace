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
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var triggerResultColumns = []string{"message_id", "action", "event_version", "status", "received_at", "lease_token", "lease_until", "model_attempts", "model_started", "result_run_id", "retry_after", "retry_failures"}

func triggerResultRows(rows ...triggerExecutionRow) *sqlmock.Rows {
	result := sqlmock.NewRows(triggerResultColumns)
	for _, row := range rows {
		result.AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, row.Status, row.ReceivedAt, row.Token, row.Until, row.ModelAttempts, row.ModelStarted, row.ResultRunID, nil, 0)
	}
	return result
}

func triggerResultScope() draftRunScope {
	return draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
}

func triggerResultKey(row triggerExecutionRow) string {
	return (model.AgentTriggerOutbox{MessageID: row.Event.MessageID}).RequestKey()
}

func expectTriggerResultLock(mock sqlmock.Sqlmock, lease TriggerLease, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnRows(rows).RowsWillBeClosed()
}

func expectTriggerResultInsert(mock sqlmock.Sqlmock, row triggerExecutionRow, runID int64, drafts []taskDraft) {
	scope := triggerResultScope()
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WithArgs(runID, scope.TeamID, scope.GroupID, scope.InitiatorID, triggerResultKey(row), testDraftFingerprint, string(draftWaitingConfirmation), "collection", len(drafts)).WillReturnResult(sqlmock.NewResult(0, 1))
	for i, draft := range drafts {
		mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(runID, i, draft)...).WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func TestTriggerResultCompletesOnlyOneBoundedTransactionAndKeepsFullDraftEvidence(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		t.Run(map[int]string{1: "first", 2: "second"}[attempts], func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			row.ModelAttempts = attempts
			caller := *row.lease()
			caller.Until = time.Time{}
			caller.ModelAttempts = 999
			caller.ModelStarted = false
			first := collectionStoreDraft()
			first.SourceMessageID = row.Event.MessageID - 1
			first.Description = "需本人审查"
			first.AssigneeName, first.AssigneeResolution = "张三", assigneeAmbiguous
			first.Deadline = draftDeadlineMetadata{Text: "晚点", Source: "instruction", ReferenceUnixMs: 1700000000123, InstructionReferenceUnixMs: 1700000000123, Timezone: draftDeadlineTimezone, Resolution: "needs_input", Reason: "unsupported_expression"}
			second := collectionStoreDraft() // Zero source is valid, and is not inferred.
			second.Title = "另一项"
			normalized := []taskDraft{first, second}
			input := append([]taskDraft(nil), normalized...)
			input[0].Title = " " + first.Title + " "
			mock.ExpectBegin()
			expectTriggerResultLock(mock, caller, triggerResultRows(row))
			expectTriggerResultInsert(mock, row, 9001, normalized)
			expectTriggerResultLock(mock, caller, triggerResultRows(row))
			mock.ExpectExec(regexp.QuoteMeta(completeTriggerDraftCollection)).WithArgs(int64(9001), row.Event.MessageID, row.Token.String, attempts).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			id, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), caller, 9001, triggerResultScope(), input, triggerResultKey(row), testDraftFingerprint)
			if err != nil || id != 9001 || input[0].Title != " "+first.Title+" " {
				t.Fatalf("complete=%d err=%v caller input=%+v", id, err, input)
			}
		})
	}
}

func TestTriggerResultRejectsUnchargedInvalidAndLostLeaseBeforeAnyDraftSQL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*triggerExecutionRow)
		absent bool
		want   error
	}{
		{"not begun", func(r *triggerExecutionRow) { r.ModelStarted = 0; r.ModelAttempts = 0 }, false, ErrInvalidTriggerState},
		{"already released", nil, true, ErrTriggerLeaseLost},
		{"expired or other owner", nil, true, ErrTriggerLeaseLost},
		{"bad result on running", func(r *triggerExecutionRow) { r.ResultRunID = sql.NullInt64{Int64: 9001, Valid: true} }, false, ErrInvalidTriggerState},
		{"bad model flag", func(r *triggerExecutionRow) { r.ModelStarted = 2 }, false, ErrInvalidTriggerState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			lease := *row.lease()
			if tc.mutate != nil {
				tc.mutate(&row)
			}
			rows := triggerResultRows(row)
			if tc.absent {
				rows = triggerResultRows()
			}
			mock.ExpectBegin()
			expectTriggerResultLock(mock, lease, rows)
			mock.ExpectRollback()
			id, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), lease, 9001, triggerResultScope(), []taskDraft{collectionStoreDraft()}, triggerResultKey(row), testDraftFingerprint)
			if id != 0 || !errors.Is(err, tc.want) {
				t.Fatalf("id=%d err=%v want=%v", id, err, tc.want)
			}
		})
	}
}

func TestTriggerResultRollsBackDraftsWhenFinalFenceOrUpdateFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		finalRow  func(triggerExecutionRow) *sqlmock.Rows
		affected  int64
		updateErr error
		want      error
		code      codes.Code
	}{
		{name: "expires during inserts", finalRow: func(triggerExecutionRow) *sqlmock.Rows { return triggerResultRows() }, want: ErrTriggerLeaseLost},
		{name: "changed saved budget", finalRow: func(r triggerExecutionRow) *sqlmock.Rows { r.ModelAttempts = 2; return triggerResultRows(r) }, want: ErrInvalidTriggerState},
		{name: "changed started", finalRow: func(r triggerExecutionRow) *sqlmock.Rows { r.ModelStarted = 0; return triggerResultRows(r) }, want: ErrInvalidTriggerState},
		{name: "conditional update lost", affected: 0, want: ErrTriggerLeaseLost},
		{name: "multiple updated rows", affected: 2, want: ErrInvalidTriggerState},
		{name: "update SQL failure", updateErr: errors.New("private SQL secret"), code: codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			lease := *row.lease()
			mock.ExpectBegin()
			expectTriggerResultLock(mock, lease, triggerResultRows(row))
			expectTriggerResultInsert(mock, row, 9001, []taskDraft{collectionStoreDraft()})
			rows := triggerResultRows(row)
			if tc.finalRow != nil {
				rows = tc.finalRow(row)
			}
			expectTriggerResultLock(mock, lease, rows)
			if tc.finalRow == nil {
				update := mock.ExpectExec(regexp.QuoteMeta(completeTriggerDraftCollection)).WithArgs(int64(9001), row.Event.MessageID, row.Token.String, row.ModelAttempts)
				if tc.updateErr != nil {
					update.WillReturnError(tc.updateErr)
				} else {
					update.WillReturnResult(sqlmock.NewResult(0, tc.affected))
				}
			}
			mock.ExpectRollback()
			id, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), lease, 9001, triggerResultScope(), []taskDraft{collectionStoreDraft()}, triggerResultKey(row), testDraftFingerprint)
			if id != 0 || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.want == nil && status.Code(err) != tc.code) || strings.Contains(err.Error(), "private") {
				t.Fatalf("id=%d err=%v", id, err)
			}
		})
	}
}

func TestTriggerResultInsertFailuresAndUncertainCommitNeverReturnRunID(t *testing.T) {
	for _, stage := range []string{"run", "duplicate run", "first item", "second item", "commit"} {
		t.Run(stage, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row := executionFixtureRow()
			lease := *row.lease()
			mock.ExpectBegin()
			expectTriggerResultLock(mock, lease, triggerResultRows(row))
			run := mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun))
			if stage == "run" {
				run.WillReturnError(errors.New("private run insert"))
			} else if stage == "duplicate run" {
				run.WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private fixed key duplicate"})
			} else {
				run.WillReturnResult(sqlmock.NewResult(0, 1))
				first := mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem))
				if stage == "first item" {
					first.WillReturnError(errors.New("private item insert"))
				} else {
					first.WillReturnResult(sqlmock.NewResult(0, 1))
					second := mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem))
					if stage == "second item" {
						second.WillReturnError(errors.New("private second item"))
					} else {
						second.WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
			}
			if stage == "commit" {
				expectTriggerResultLock(mock, lease, triggerResultRows(row))
				mock.ExpectExec(regexp.QuoteMeta(completeTriggerDraftCollection)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit().WillReturnError(errors.New("private ambiguous commit"))
			} else {
				mock.ExpectRollback()
			}
			id, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), lease, 9001, triggerResultScope(), []taskDraft{collectionStoreDraft(), collectionStoreDraft()}, triggerResultKey(row), testDraftFingerprint)
			if id != 0 || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), lease.Token) {
				t.Fatalf("id=%d err=%v", id, err)
			}
		})
	}
}

func TestTriggerResultPreflightInvalidInputNeverStartsSQL(t *testing.T) {
	for _, name := range []string{"nil context", "cancelled", "expired context", "bad lease", "bad run", "bad team", "bad group", "bad actor", "wrong source key", "caller key", "bad fingerprint", "empty collection", "six items", "invalid second", "missing assignee metadata", "missing deadline metadata"} {
		t.Run(name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			_ = mock
			row := executionFixtureRow()
			lease := *row.lease()
			scope := triggerResultScope()
			id := int64(9001)
			key := triggerResultKey(row)
			fingerprint := testDraftFingerprint
			items := []taskDraft{collectionStoreDraft()}
			ctx := context.Background()
			want := codes.InvalidArgument
			switch name {
			case "nil context":
				ctx = nil
			case "cancelled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
				want = codes.Canceled
			case "expired context":
				c, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = c
				want = codes.DeadlineExceeded
			case "bad lease":
				lease.Token = "forged"
			case "bad run":
				id = 0
			case "bad team":
				scope.TeamID = 0
			case "bad group":
				scope.GroupID = -1
			case "bad actor":
				scope.InitiatorID = 0
			case "wrong source key":
				key = (model.AgentTriggerOutbox{MessageID: row.Event.MessageID + 1}).RequestKey()
			case "caller key":
				key = "browser-random-key"
			case "bad fingerprint":
				fingerprint = "short"
			case "empty collection":
				items = nil
			case "six items":
				items = make([]taskDraft, 6)
				for i := range items {
					items[i] = collectionStoreDraft()
				}
			case "invalid second":
				items = append(items, collectionStoreDraft())
				items[1].SourceMessageID = -1
			case "missing assignee metadata":
				items[0].AssigneeResolution = ""
			case "missing deadline metadata":
				items[0].Deadline = draftDeadlineMetadata{}
			}
			result, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(ctx, lease, id, scope, items, key, fingerprint)
			if result != 0 || status.Code(err) != want {
				t.Fatalf("id=%d err=%v want=%v", result, err, want)
			}
		})
	}
	row := executionFixtureRow()
	for _, store := range []*TriggerInboxStore{nil, {}} {
		result, err := store.CompleteDraftCollection(context.Background(), *row.lease(), 9001, triggerResultScope(), []taskDraft{collectionStoreDraft()}, triggerResultKey(row), testDraftFingerprint)
		if result != 0 || status.Code(err) != codes.Unavailable {
			t.Fatalf("unavailable store id=%d err=%v", result, err)
		}
	}
}

func TestTriggerResultCompletedShapeAllowsOnlyBudgetAndPositiveResultWithoutLease(t *testing.T) {
	for _, state := range []string{TriggerInboxQueued, TriggerInboxRunning, TriggerInboxExhausted, TriggerInboxCompleted} {
		for _, result := range []sql.NullInt64{{}, {Int64: 0, Valid: true}, {Int64: -1, Valid: true}, {Int64: 9001, Valid: true}} {
			row := executionFixtureRow()
			row.Status = state
			row.ResultRunID = result
			if state != TriggerInboxRunning {
				row.Token = sql.NullString{}
				row.Until = sql.NullTime{}
				row.ModelStarted = 0
			}
			if state == TriggerInboxExhausted {
				row.ModelAttempts = 2
			}
			want := (state == TriggerInboxCompleted && result.Valid && result.Int64 > 0) || (state != TriggerInboxCompleted && !result.Valid)
			if got := validateTriggerExecutionRow(&row) == nil; got != want {
				t.Fatalf("state=%s result=%v valid=%v want=%v", state, result, got, want)
			}
		}
	}
	for _, mutate := range []func(*triggerExecutionRow){
		func(r *triggerExecutionRow) { r.ModelAttempts = 0 }, func(r *triggerExecutionRow) { r.ModelAttempts = 3 }, func(r *triggerExecutionRow) { r.ModelStarted = 1 },
		func(r *triggerExecutionRow) { r.Token = sql.NullString{String: strings.Repeat("a", 64), Valid: true} }, func(r *triggerExecutionRow) { r.Until = sql.NullTime{Time: time.Now(), Valid: true} },
	} {
		row := executionFixtureRow()
		row.Status = TriggerInboxCompleted
		row.Token = sql.NullString{}
		row.Until = sql.NullTime{}
		row.ModelStarted = 0
		row.ResultRunID = sql.NullInt64{Int64: 9001, Valid: true}
		mutate(&row)
		if !errors.Is(validateTriggerExecutionRow(&row), ErrInvalidTriggerState) {
			t.Fatalf("invalid completed accepted: %+v", row)
		}
	}
}

func TestTriggerResultCompletedNotificationReplayNeverResetsResultOrRegainsLease(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	lease := *row.lease()
	row.Status = TriggerInboxCompleted
	row.Token = sql.NullString{}
	row.Until = sql.NullTime{}
	row.ModelStarted = 0
	row.ResultRunID = sql.NullInt64{Int64: 9001, Valid: true}
	store := NewTriggerInboxStore(drafts.db)
	for i := 0; i < 2; i++ {
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(row.Event.MessageID, row.Event.Action, row.Event.Version, TriggerInboxQueued).WillReturnError(&mysql.MySQLError{Number: 1062})
		mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(row.Event.MessageID).WillReturnRows(sqlmock.NewRows([]string{"message_id", "action", "event_version", "status", "received_at", "result_run_id"}).AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, row.Status, row.ReceivedAt, row.ResultRunID))
		mock.ExpectCommit()
		if err := store.Accept(context.Background(), row.Event); err != nil {
			t.Fatal(err)
		}
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(triggerResultRows())
	mock.ExpectCommit()
	if next, err := store.Claim(context.Background()); next != nil || err != nil {
		t.Fatalf("completed claimed: %+v %v", next, err)
	}
	mock.ExpectBegin()
	expectTriggerResultLock(mock, lease, triggerResultRows())
	mock.ExpectRollback()
	if id, err := store.CompleteDraftCollection(context.Background(), lease, 9002, triggerResultScope(), []taskDraft{collectionStoreDraft()}, triggerResultKey(row), testDraftFingerprint); id != 0 || !errors.Is(err, ErrTriggerLeaseLost) {
		t.Fatalf("completed regenerated: %d %v", id, err)
	}
}

func TestTriggerResultReceiptRejectsInvalidResultAssociationWithoutUpdate(t *testing.T) {
	for _, tc := range []struct {
		state  string
		result driver.Value
	}{{TriggerInboxCompleted, nil}, {TriggerInboxCompleted, int64(0)}, {TriggerInboxCompleted, int64(-1)}, {TriggerInboxRunning, int64(9001)}, {TriggerInboxQueued, int64(9001)}, {TriggerInboxExhausted, int64(9001)}} {
		drafts, mock := testDraftStore(t)
		row := executionFixtureRow()
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WillReturnError(&mysql.MySQLError{Number: 1062})
		mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(row.Event.MessageID).WillReturnRows(sqlmock.NewRows([]string{"message_id", "action", "event_version", "status", "received_at", "result_run_id"}).AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, tc.state, row.ReceivedAt, tc.result))
		mock.ExpectRollback()
		if err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), row.Event); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("state=%s result=%v err=%v", tc.state, tc.result, err)
		}
	}
}

func TestTriggerResultSQLCancellationRollsBackBeforeAnyCompletion(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillDelayFor(80 * time.Millisecond).WillReturnRows(triggerResultRows(row))
	mock.ExpectRollback()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	id, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(ctx, *row.lease(), 9001, triggerResultScope(), []taskDraft{collectionStoreDraft()}, triggerResultKey(row), testDraftFingerprint)
	if id != 0 || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("id=%d err=%v", id, err)
	}
	waitTriggerInboxRollback(t, mock)
}
