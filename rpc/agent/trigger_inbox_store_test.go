package agent

import (
	"context"
	"errors"
	"math"
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

var triggerInboxTestColumns = []string{"message_id", "action", "event_version", "status", "received_at"}

func triggerInboxTestEvent() model.AgentTriggerEvent {
	return model.AgentTriggerEvent{MessageID: 9007199254740993, Action: model.AgentTriggerAction, Version: model.AgentTriggerVersion}
}

func triggerInboxRows(event model.AgentTriggerEvent, savedStatus string, receivedAt any) *sqlmock.Rows {
	return sqlmock.NewRows(triggerInboxTestColumns).AddRow(event.MessageID, event.Action, int64(event.Version), savedStatus, receivedAt)
}

func TestTriggerInboxStoreCommitsOnlyImmutableQueuedNotification(t *testing.T) {
	for _, messageID := range []int64{1, 9007199254740993, math.MaxInt64} {
		drafts, mock := testDraftStore(t)
		event := triggerInboxTestEvent()
		event.MessageID = messageID
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTriggerInboxStoreRepeatedReceiptKeepsOriginalQueuedTimeWithoutUpdate(t *testing.T) {
	drafts, mock := testDraftStore(t)
	event := triggerInboxTestEvent()
	oldReceipt := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
	for n := 0; n < 2; n++ {
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private duplicate key"})
		mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).WillReturnRows(triggerInboxRows(event, TriggerInboxQueued, oldReceipt))
		mock.ExpectCommit()
	}
	store := NewTriggerInboxStore(drafts.db)
	for n := 0; n < 2; n++ {
		if err := store.Accept(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTriggerInboxStoreRejectsBadPersistedFactsWithoutResettingRow(t *testing.T) {
	for _, name := range []string{"missing", "different ID", "different action", "different version", "processing status", "empty status", "zero time", "before epoch", "NULL time", "NULL status", "NULL ID", "NULL action", "NULL version", "bad timestamp", "multiple"} {
		t.Run(name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			event, saved := triggerInboxTestEvent(), triggerInboxTestEvent()
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WillReturnError(&mysql.MySQLError{Number: 1062})
			at := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
			state := TriggerInboxQueued
			switch name {
			case "different ID":
				saved.MessageID++
			case "different action":
				saved.Action = "other"
			case "different version":
				saved.Version = 2
			case "processing status":
				state = "processing"
			case "empty status":
				state = ""
			case "zero time":
				at = time.Time{}
			case "before epoch":
				at = time.Unix(-1, 0)
			}
			rows := triggerInboxRows(saved, state, at)
			switch name {
			case "missing":
				rows = sqlmock.NewRows(triggerInboxTestColumns)
			case "multiple":
				rows.AddRow(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued, at)
			case "NULL time":
				rows = triggerInboxRows(saved, state, nil)
			case "NULL status":
				rows = sqlmock.NewRows(triggerInboxTestColumns).AddRow(saved.MessageID, saved.Action, int64(saved.Version), nil, at)
			case "NULL ID":
				rows = sqlmock.NewRows(triggerInboxTestColumns).AddRow(nil, saved.Action, int64(saved.Version), state, at)
			case "NULL action":
				rows = sqlmock.NewRows(triggerInboxTestColumns).AddRow(saved.MessageID, nil, int64(saved.Version), state, at)
			case "NULL version":
				rows = sqlmock.NewRows(triggerInboxTestColumns).AddRow(saved.MessageID, saved.Action, nil, state, at)
			case "bad timestamp":
				rows = triggerInboxRows(saved, state, "private invalid timestamp")
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).WillReturnRows(rows)
			mock.ExpectRollback()
			err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), event)
			if status.Code(err) != codes.FailedPrecondition || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("invalid saved receipt=%v", err)
			}
		})
	}
}

func TestTriggerInboxStoreRejectsSQLAndCommitFailuresWithoutFalseSuccess(t *testing.T) {
	for _, name := range []string{"begin", "insert", "not duplicate mysql", "wrapped duplicate", "zero affected", "two affected", "query", "row error", "commit", "duplicate commit"} {
		t.Run(name, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			event := triggerInboxTestEvent()
			if name == "begin" {
				mock.ExpectBegin().WillReturnError(errors.New("private database connection"))
			} else {
				mock.ExpectBegin()
				exec := mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued)
				duplicate := name == "query" || name == "row error" || name == "duplicate commit" || name == "wrapped duplicate"
				switch {
				case duplicate:
					err := error(&mysql.MySQLError{Number: 1062})
					if name == "wrapped duplicate" {
						err = errors.Join(errors.New("private wrapper"), err)
					}
					exec.WillReturnError(err)
					query := mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID)
					if name == "query" {
						query.WillReturnError(errors.New("private database query"))
					} else {
						rows := triggerInboxRows(event, TriggerInboxQueued, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
						if name == "row error" {
							rows.RowError(0, errors.New("private iteration"))
						}
						query.WillReturnRows(rows)
					}
				case name == "insert":
					exec.WillReturnError(errors.New("private SQL secret"))
				case name == "not duplicate mysql":
					exec.WillReturnError(&mysql.MySQLError{Number: 1146, Message: "private missing table"})
				default:
					count := int64(1)
					if name == "zero affected" {
						count = 0
					} else if name == "two affected" {
						count = 2
					}
					exec.WillReturnResult(sqlmock.NewResult(0, count))
				}
				if name == "commit" || name == "duplicate commit" {
					mock.ExpectCommit().WillReturnError(errors.New("private commit"))
				} else if name == "wrapped duplicate" {
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
			}
			err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), event)
			want := codes.Unavailable
			if name == "wrapped duplicate" {
				want = codes.OK
			}
			if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("failed storage result=%v want=%v", err, want)
			}
		})
	}
}

func TestTriggerInboxStoreValidatesInputAndCancellationBeforeDatabase(t *testing.T) {
	drafts, _ := testDraftStore(t)
	store := NewTriggerInboxStore(drafts.db)
	event := triggerInboxTestEvent()
	for _, invalid := range []model.AgentTriggerEvent{{}, {MessageID: -1, Action: model.AgentTriggerAction, Version: 1}, {MessageID: 1, Action: "other", Version: 1}, {MessageID: 1, Action: model.AgentTriggerAction, Version: 2}} {
		if err := store.Accept(context.Background(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bad input=%v", err)
		}
	}
	if err := store.Accept(nil, event); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil context=%v", err)
	}
	var absent *TriggerInboxStore
	if err := absent.Accept(context.Background(), event); status.Code(err) != codes.Unavailable {
		t.Fatalf("nil store=%v", err)
	}
	if err := NewTriggerInboxStore(nil).Accept(context.Background(), event); status.Code(err) != codes.Unavailable {
		t.Fatalf("nil DB=%v", err)
	}
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := codes.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = codes.DeadlineExceeded
		} else {
			cancel()
		}
		for _, invalid := range []model.AgentTriggerEvent{event, {}} {
			if err := store.Accept(ctx, invalid); status.Code(err) != want {
				t.Fatalf("context priority=%v", err)
			}
		}
		cancel()
	}
}

func TestTriggerInboxStoreHonorsDeadlineDuringInsertAndDuplicateRead(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		drafts, mock := testDraftStore(t)
		event := triggerInboxTestEvent()
		mock.ExpectBegin()
		exec := mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox))
		if duplicate {
			exec.WillReturnError(&mysql.MySQLError{Number: 1062})
			mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).
				WillDelayFor(100 * time.Millisecond).WillReturnRows(triggerInboxRows(event, TriggerInboxQueued, time.Now().Truncate(time.Second)))
		} else {
			exec.WillDelayFor(100 * time.Millisecond).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectRollback()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := NewTriggerInboxStore(drafts.db).Accept(ctx, event)
		cancel()
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("SQL timeout=%v", err)
		}
	}
}

func TestTriggerInboxReplayKeepsRunningAndExhaustedReceiptWithoutAnyUpdate(t *testing.T) {
	for _, savedStatus := range []string{TriggerInboxRunning, TriggerInboxExhausted} {
		t.Run(savedStatus, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			event := triggerInboxTestEvent()
			oldReceipt := time.Date(2026, 9, 1, 1, 2, 3, 123456000, time.UTC)
			store := NewTriggerInboxStore(drafts.db)
			for n := 0; n < 3; n++ {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).
					WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private duplicate key"})
				mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).
					WillReturnRows(triggerInboxRows(event, savedStatus, oldReceipt))
				// No UPDATE is expected: neither execution fields nor received_at
				// may change, even after repeated notification acknowledgements.
				mock.ExpectCommit()
				if err := store.Accept(context.Background(), event); err != nil {
					t.Fatalf("replay %d of %s: %v", n, savedStatus, err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestTriggerInboxReplayRejectsUnknownAndMalformedExecutionStatus(t *testing.T) {
	for _, savedStatus := range []string{"succeeded", "failed", "completed", "Running", "running ", " exhausted", "running\x00", "exhausted\n", "\xff"} {
		t.Run(savedStatus, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			event := triggerInboxTestEvent()
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).
				WillReturnError(&mysql.MySQLError{Number: 1062})
			mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).
				WillReturnRows(triggerInboxRows(event, savedStatus, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
			mock.ExpectRollback()
			if err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), event); status.Code(err) != codes.FailedPrecondition || strings.Contains(err.Error(), savedStatus) {
				t.Fatalf("unsafe malformed-state result: %v", err)
			}
		})
	}
}

func TestTriggerInboxReplayExecutionStatusStillRequiresImmutableFactsAndValidReceiptTime(t *testing.T) {
	for _, savedStatus := range []string{TriggerInboxRunning, TriggerInboxExhausted} {
		for _, changed := range []string{"message ID", "action", "version", "NULL time", "nonpositive time"} {
			t.Run(savedStatus+"/"+changed, func(t *testing.T) {
				drafts, mock := testDraftStore(t)
				event, saved := triggerInboxTestEvent(), triggerInboxTestEvent()
				var receivedAt any = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
				switch changed {
				case "message ID":
					saved.MessageID++
				case "action":
					saved.Action = "private-other-action"
				case "version":
					saved.Version++
				case "NULL time":
					receivedAt = nil
				case "nonpositive time":
					receivedAt = time.Unix(0, 999999)
				}
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).
					WillReturnError(&mysql.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).
					WillReturnRows(triggerInboxRows(saved, savedStatus, receivedAt))
				mock.ExpectRollback()
				if err := NewTriggerInboxStore(drafts.db).Accept(context.Background(), event); status.Code(err) != codes.FailedPrecondition || strings.Contains(err.Error(), "private") {
					t.Fatalf("changed receipt accepted: %v", err)
				}
			})
		}
	}
}

func TestTriggerInboxReplayExecutionStatusCommitFailureIsSafeAndSameFactsCanReplayAgain(t *testing.T) {
	for _, savedStatus := range []string{TriggerInboxRunning, TriggerInboxExhausted} {
		t.Run(savedStatus, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			event := triggerInboxTestEvent()
			oldReceipt := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
			store := NewTriggerInboxStore(drafts.db)
			for attempt := 0; attempt < 2; attempt++ {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).
					WillReturnError(&mysql.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).
					WillReturnRows(triggerInboxRows(event, savedStatus, oldReceipt))
				commit := mock.ExpectCommit()
				want := codes.OK
				if attempt == 0 {
					commit.WillReturnError(errors.New("private uncertain commit"))
					want = codes.Unavailable
				}
				err := store.Accept(context.Background(), event)
				if status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") {
					t.Fatalf("attempt %d: %v want=%v", attempt, err, want)
				}
			}
		})
	}
}

func TestTriggerInboxReplayExecutionReadCancellationWinsOverBadFacts(t *testing.T) {
	for _, savedStatus := range []string{TriggerInboxRunning, TriggerInboxExhausted} {
		for _, deadline := range []bool{false, true} {
			t.Run(savedStatus+map[bool]string{false: "/cancel", true: "/deadline"}[deadline], func(t *testing.T) {
				drafts, mock := testDraftStore(t)
				event, bad := triggerInboxTestEvent(), triggerInboxTestEvent()
				bad.Action = "private-corrupt-action"
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(event.MessageID, event.Action, int64(event.Version), TriggerInboxQueued).
					WillReturnError(&mysql.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(event.MessageID).WillDelayFor(100 * time.Millisecond).
					WillReturnRows(triggerInboxRows(bad, savedStatus, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
				mock.ExpectRollback()
				ctx, cancel := context.WithCancel(context.Background())
				want := codes.Canceled
				var timer *time.Timer
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
					want = codes.DeadlineExceeded
				} else {
					timer = time.AfterFunc(20*time.Millisecond, cancel)
					defer timer.Stop()
				}
				defer cancel()
				if err := NewTriggerInboxStore(drafts.db).Accept(ctx, event); status.Code(err) != want || strings.Contains(err.Error(), "private") {
					t.Fatalf("cancellation lost priority: %v", err)
				}
				// database/sql can finish rollback asynchronously after cancellation.
				// Wait for the actual expected rollback before test cleanup closes DB.
				until := time.Now().Add(time.Second)
				for {
					if err := mock.ExpectationsWereMet(); err == nil {
						break
					} else if !time.Now().Before(until) {
						t.Fatalf("cancelled transaction did not finish rollback: %v", err)
					}
					time.Sleep(time.Millisecond)
				}
			})
		}
	}
}
