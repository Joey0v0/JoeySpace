package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func taskTriggerStatusRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"message_id", "status", "result_run_id"})
}

func TestTaskTriggerStatusStoreReadsFourStatesWithoutTransactionsOrWrites(t *testing.T) {
	for _, state := range []string{TriggerInboxQueued, TriggerInboxRunning, TriggerInboxExhausted, TriggerInboxCompleted} {
		t.Run(state, func(t *testing.T) {
			draftStore, mock := testDraftStore(t)
			store := NewTriggerInboxStore(draftStore.db)
			const messageID int64 = 9007199254740993
			var run interface{}
			wantRunID := int64(0)
			if state == TriggerInboxCompleted {
				wantRunID = math.MaxInt64
				run = wantRunID
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectTaskTriggerStatus)).WithArgs(messageID).
				WillReturnRows(taskTriggerStatusRows().AddRow(messageID, state, run)).RowsWillBeClosed()
			got, err := store.loadTaskTriggerStatus(context.Background(), messageID)
			if err != nil || got != (taskTriggerStatus{MessageID: messageID, Status: state, RunID: wantRunID}) {
				t.Fatalf("status=%+v err=%v", got, err)
			}
		})
	}
}

func TestTaskTriggerStatusStoreRejectsMalformedStateOrResultCombinations(t *testing.T) {
	for _, name := range []string{"NULL message", "wrong message", "NULL state", "empty state", "unknown state", "queued zero result", "queued positive result", "running result", "exhausted result", "completed NULL", "completed zero", "completed negative", "completed overflow", "duplicate", "row error"} {
		t.Run(name, func(t *testing.T) {
			draftStore, mock := testDraftStore(t)
			var message interface{} = int64(42)
			var state interface{} = TriggerInboxQueued
			var run interface{}
			switch name {
			case "NULL message":
				message = nil
			case "wrong message":
				message = int64(43)
			case "NULL state":
				state = nil
			case "empty state":
				state = ""
			case "unknown state":
				state = "failed"
			case "queued zero result":
				run = int64(0)
			case "queued positive result":
				run = int64(500)
			case "running result":
				state, run = TriggerInboxRunning, int64(500)
			case "exhausted result":
				state, run = TriggerInboxExhausted, int64(500)
			case "completed NULL":
				state = TriggerInboxCompleted
			case "completed zero":
				state, run = TriggerInboxCompleted, int64(0)
			case "completed negative":
				state, run = TriggerInboxCompleted, int64(-1)
			case "completed overflow":
				state, run = TriggerInboxCompleted, "9223372036854775808"
			}
			rows := taskTriggerStatusRows().AddRow(message, state, run)
			if name == "duplicate" {
				rows.AddRow(message, state, run)
			}
			if name == "row error" {
				rows.RowError(0, errors.New("private SQL and token"))
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectTaskTriggerStatus)).WithArgs(int64(42)).WillReturnRows(rows).RowsWillBeClosed()
			got, err := NewTriggerInboxStore(draftStore.db).loadTaskTriggerStatus(context.Background(), 42)
			if got != (taskTriggerStatus{}) || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
				t.Fatalf("damaged state exposed: %+v %v", got, err)
			}
		})
	}
}

func TestTaskTriggerStatusStoreMissingAndSQLFailuresReturnNoState(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			draftStore, mock := testDraftStore(t)
			query := mock.ExpectQuery(regexp.QuoteMeta(selectTaskTriggerStatus)).WithArgs(int64(42))
			want := codes.Unavailable
			if missing {
				want = codes.NotFound
				query.WillReturnRows(taskTriggerStatusRows())
			} else {
				query.WillReturnError(errors.New("private SQL and password"))
			}
			got, err := NewTriggerInboxStore(draftStore.db).loadTaskTriggerStatus(context.Background(), 42)
			if got != (taskTriggerStatus{}) || status.Code(err) != want || strings.Contains(err.Error(), "private") {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}

func TestTaskTriggerStatusStoreRejectsInvalidContextIDsAndDisabledStorage(t *testing.T) {
	draftStore, _ := testDraftStore(t)
	store := NewTriggerInboxStore(draftStore.db)
	for _, id := range []int64{0, -1} {
		if got, err := store.loadTaskTriggerStatus(context.Background(), id); got != (taskTriggerStatus{}) || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid ID=%+v %v", got, err)
		}
	}
	if got, err := store.loadTaskTriggerStatus(nil, 42); got != (taskTriggerStatus{}) || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil context=%+v %v", got, err)
	}
	for _, disabled := range []*TriggerInboxStore{nil, NewTriggerInboxStore(nil)} {
		if got, err := disabled.loadTaskTriggerStatus(context.Background(), 42); got != (taskTriggerStatus{}) || status.Code(err) != codes.Unavailable {
			t.Fatalf("disabled=%+v %v", got, err)
		}
	}
}

func TestTaskTriggerStatusStoreCancellationAndCallerDeadlineTakePriority(t *testing.T) {
	for _, expired := range []bool{false, true} {
		draftStore, _ := testDraftStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		want := codes.Canceled
		cancel()
		if expired {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = codes.DeadlineExceeded
		}
		got, err := NewTriggerInboxStore(draftStore.db).loadTaskTriggerStatus(ctx, 0)
		cancel()
		if got != (taskTriggerStatus{}) || status.Code(err) != want {
			t.Fatalf("context=%+v %v", got, err)
		}
	}
	draftStore, mock := testDraftStore(t)
	mock.ExpectQuery(regexp.QuoteMeta(selectTaskTriggerStatus)).WithArgs(int64(42)).WillDelayFor(100 * time.Millisecond).
		WillReturnRows(taskTriggerStatusRows().AddRow(42, TriggerInboxQueued, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got, err := NewTriggerInboxStore(draftStore.db).loadTaskTriggerStatus(ctx, 42); got != (taskTriggerStatus{}) || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("deadline=%+v %v", got, err)
	}
}
