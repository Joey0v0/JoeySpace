package main

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const outboxPendingSQL = "SELECT notification_id, team_id, recipient_id, event_version, published FROM `task_notification_outbox` WHERE published = ? ORDER BY notification_id ASC LIMIT ?"
const outboxLockSQL = "SELECT notification_id, team_id, recipient_id, event_version, published FROM `task_notification_outbox` WHERE notification_id = ? LIMIT ? FOR UPDATE"
const outboxPublishSQL = "UPDATE `task_notification_outbox` SET `published`=?,`published_at`=CURRENT_TIMESTAMP WHERE notification_id = ? AND team_id = ? AND recipient_id = ? AND event_version = ? AND published = ?"

func outboxRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"notification_id", "team_id", "recipient_id", "event_version", "published"})
}

func pendingOutboxRow() taskNotificationOutboxRow {
	return taskNotificationOutboxRow{NotificationID: math.MaxInt64, TeamID: 200, RecipientID: 42, EventVersion: 1}
}

func expectOutboxLockedRow(mock sqlmock.Sqlmock, row taskNotificationOutboxRow) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(outboxLockSQL)).WithArgs(row.NotificationID, 1).
		WillReturnRows(outboxRows().AddRow(row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, row.Published))
}

func TestNotificationOutboxStorePendingAscendingCapacityAndStableFacts(t *testing.T) {
	s, mock := testTaskServer(t)
	store := newTaskNotificationOutboxStore(s.db)
	mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 2).
		WillReturnRows(outboxRows().AddRow(1, 200, 42, 1, false).AddRow(math.MaxInt64, 200, 77, 1, false))
	rows, err := store.ListPending(context.Background(), 2)
	if err != nil || len(rows) != 2 || rows[0].NotificationID != 1 || rows[1].NotificationID != math.MaxInt64 || rows[1].TeamID != 200 || rows[1].RecipientID != 77 || rows[1].Published {
		t.Fatalf("pending facts=%v error=%v", rows, err)
	}
	if rows[1].Event().Key() != "task-notification:9223372036854775807" {
		t.Fatalf("stable event key=%s", rows[1].Event().Key())
	}
}

func TestNotificationOutboxStorePendingEmptyAndMaximumLimit(t *testing.T) {
	for _, limit := range []int{1, 100} {
		s, mock := testTaskServer(t)
		mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, limit).WillReturnRows(outboxRows())
		rows, err := newTaskNotificationOutboxStore(s.db).ListPending(context.Background(), limit)
		if err != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("empty pending=%v error=%v", rows, err)
		}
	}
}

func TestNotificationOutboxStoreRejectsBadLimitAndMissingDatabaseBeforeSQL(t *testing.T) {
	s, _ := testTaskServer(t)
	store := newTaskNotificationOutboxStore(s.db)
	for _, limit := range []int{-1, 0, 101} {
		rows, err := store.ListPending(context.Background(), limit)
		if rows != nil || err == nil {
			t.Fatalf("invalid capacity %d: %v %v", limit, rows, err)
		}
	}
	missing := newTaskNotificationOutboxStore(nil)
	if rows, err := missing.ListPending(context.Background(), 1); rows != nil || err == nil {
		t.Fatalf("missing pending DB=%v error=%v", rows, err)
	}
	if err := missing.MarkPublished(context.Background(), pendingOutboxRow()); err == nil {
		t.Fatal("missing confirmation DB accepted")
	}
}

func TestNotificationOutboxStoreRejectsWholeInvalidBatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{"zero notification", outboxRows().AddRow(0, 200, 42, 1, false)},
		{"zero team", outboxRows().AddRow(1, 0, 42, 1, false)},
		{"zero recipient", outboxRows().AddRow(1, 200, 0, 1, false)},
		{"unknown version", outboxRows().AddRow(1, 200, 42, 2, false)},
		{"already published", outboxRows().AddRow(1, 200, 42, 1, true)},
		{"descending", outboxRows().AddRow(2, 200, 42, 1, false).AddRow(1, 200, 77, 1, false)},
		{"duplicate", outboxRows().AddRow(1, 200, 42, 1, false).AddRow(1, 200, 42, 1, false)},
		{"over capacity", outboxRows().AddRow(1, 200, 42, 1, false).AddRow(2, 200, 77, 1, false).AddRow(3, 200, 42, 1, false)},
		{"scan failure", outboxRows().AddRow("private-not-an-ID", 200, 42, 1, false)},
		{"stream error", outboxRows().AddRow(1, 200, 42, 1, false).RowError(0, errors.New("private stream failure"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testTaskServer(t)
			mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 2).WillReturnRows(tc.rows)
			rows, err := newTaskNotificationOutboxStore(s.db).ListPending(context.Background(), 2)
			if rows != nil || err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid entire batch=%v error=%v", rows, err)
			}
		})
	}
}

func TestNotificationOutboxStoreMissingMigrationFailsSafely(t *testing.T) {
	s, mock := testTaskServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 100).WillReturnError(errors.New("private table task_notification_outbox does not exist"))
	rows, err := newTaskNotificationOutboxStore(s.db).ListPending(context.Background(), 100)
	if rows != nil || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("missing migration=%v error=%v", rows, err)
	}
}

func TestNotificationOutboxStoreMarkPublishedFirstTimeAndReplayKeepOriginalTime(t *testing.T) {
	s, mock := testTaskServer(t)
	row := pendingOutboxRow()
	expectOutboxLockedRow(mock, row)
	mock.ExpectExec(regexp.QuoteMeta(outboxPublishSQL)).WithArgs(true, row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err != nil {
		t.Fatalf("initial publish confirmation=%v", err)
	}
	// A second publisher can hold the old pending row. On restart, persisted facts win.
	published := row
	published.Published = true
	expectOutboxLockedRow(mock, published)
	mock.ExpectCommit()
	if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err != nil {
		t.Fatalf("published replay=%v", err)
	}
	// No second UPDATE is admitted, so first published_at cannot be overwritten.
}

func TestNotificationOutboxStoreMarkPublishedUncertainCommitAndRebuildReplay(t *testing.T) {
	s, mock := testTaskServer(t)
	row := pendingOutboxRow()
	expectOutboxLockedRow(mock, row)
	mock.ExpectExec(regexp.QuoteMeta(outboxPublishSQL)).WithArgs(true, row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("private commit ACK lost"))
	if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("uncertain confirmation=%v", err)
	}
	// Model the committed-but-response-lost branch; retry only verifies its original facts.
	published := row
	published.Published = true
	expectOutboxLockedRow(mock, published)
	mock.ExpectCommit()
	if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err != nil {
		t.Fatalf("uncertain replay=%v", err)
	}
}

func TestNotificationOutboxStoreMarkPublishedRejectsChangedFactsEvenWhenAlreadyPublished(t *testing.T) {
	for _, published := range []bool{false, true} {
		for _, mutate := range []func(*taskNotificationOutboxRow){
			func(row *taskNotificationOutboxRow) { row.NotificationID-- },
			func(row *taskNotificationOutboxRow) { row.TeamID++ },
			func(row *taskNotificationOutboxRow) { row.RecipientID++ },
			func(row *taskNotificationOutboxRow) { row.EventVersion++ },
		} {
			s, mock := testTaskServer(t)
			row, stored := pendingOutboxRow(), pendingOutboxRow()
			mutate(&stored)
			stored.Published = published
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(outboxLockSQL)).WithArgs(row.NotificationID, 1).
				WillReturnRows(outboxRows().AddRow(stored.NotificationID, stored.TeamID, stored.RecipientID, stored.EventVersion, stored.Published))
			mock.ExpectRollback()
			if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err == nil {
				t.Fatalf("changed facts were accepted: requested=%v stored=%v", row, stored)
			}
		}
	}
}

func TestNotificationOutboxStoreMarkPublishedRejectsInvalidInputBeforeSQL(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, mutate := range []func(*taskNotificationOutboxRow){
		func(row *taskNotificationOutboxRow) { row.NotificationID = 0 },
		func(row *taskNotificationOutboxRow) { row.TeamID = -1 },
		func(row *taskNotificationOutboxRow) { row.RecipientID = 0 },
		func(row *taskNotificationOutboxRow) { row.EventVersion = 2 },
	} {
		row := pendingOutboxRow()
		mutate(&row)
		if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err == nil {
			t.Fatalf("invalid row accepted: %v", row)
		}
	}
}

func TestNotificationOutboxStoreMarkPublishedTransactionFailures(t *testing.T) {
	for _, failure := range []string{"begin", "missing row", "missing migration", "write", "zero affected", "multiple affected"} {
		t.Run(failure, func(t *testing.T) {
			s, mock := testTaskServer(t)
			row := pendingOutboxRow()
			if failure == "begin" {
				mock.ExpectBegin().WillReturnError(errors.New("private begin failure"))
			} else if failure == "missing row" || failure == "missing migration" {
				mock.ExpectBegin()
				query := mock.ExpectQuery(regexp.QuoteMeta(outboxLockSQL)).WithArgs(row.NotificationID, 1)
				if failure == "missing row" {
					query.WillReturnRows(outboxRows())
				} else {
					query.WillReturnError(errors.New("private table missing"))
				}
				mock.ExpectRollback()
			} else {
				expectOutboxLockedRow(mock, row)
				update := mock.ExpectExec(regexp.QuoteMeta(outboxPublishSQL)).WithArgs(true, row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, false)
				switch failure {
				case "write":
					update.WillReturnError(errors.New("private update failure"))
				case "zero affected":
					update.WillReturnResult(sqlmock.NewResult(0, 0))
				default:
					update.WillReturnResult(sqlmock.NewResult(0, 2))
				}
				mock.ExpectRollback()
			}
			if err := newTaskNotificationOutboxStore(s.db).MarkPublished(context.Background(), row); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("failed confirmation=%v", err)
			}
		})
	}
}

func TestNotificationOutboxStoreCancellationBeforeSQL(t *testing.T) {
	s, _ := testTaskServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newTaskNotificationOutboxStore(s.db)
	if rows, err := store.ListPending(ctx, 100); rows != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%v error=%v", rows, err)
	}
	if err := store.MarkPublished(ctx, pendingOutboxRow()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mark=%v", err)
	}
}

func TestNotificationOutboxStoreReadAndLockedConfirmationDeadlines(t *testing.T) {
	for _, mark := range []bool{false, true} {
		s, mock := testTaskServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		store := newTaskNotificationOutboxStore(s.db)
		if !mark {
			mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 100).
				WillDelayFor(time.Second).WillReturnRows(outboxRows())
			rows, err := store.ListPending(ctx, 100)
			if rows != nil || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pending deadline=%v error=%v", rows, err)
			}
		} else {
			row := pendingOutboxRow()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(outboxLockSQL)).WithArgs(row.NotificationID, 1).
				WillDelayFor(time.Second).WillReturnRows(outboxRows().AddRow(row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, false))
			mock.ExpectRollback()
			if err := store.MarkPublished(ctx, row); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("locked confirmation deadline=%v", err)
			}
			// database/sql may finish its cancellation rollback on another goroutine.
			// Wait for that observed rollback, rather than racing test cleanup.
			deadline := time.NewTimer(time.Second)
			ticks := time.NewTicker(time.Millisecond)
			for mock.ExpectationsWereMet() != nil {
				select {
				case <-ticks.C:
				case <-deadline.C:
					ticks.Stop()
					t.Fatal("cancellation rollback was not observed")
				}
			}
			deadline.Stop()
			ticks.Stop()
		}
	}
}
