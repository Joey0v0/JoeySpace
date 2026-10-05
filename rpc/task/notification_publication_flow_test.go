package main

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/segmentio/kafka-go"
)

func TestTaskNotificationStatusToPublicationRecoversWithSameEvent(t *testing.T) {
	s, mock := statusTaskServer(t, 42, 0)
	expectTaskStatusRow(mock, 42, int64(77), 0)
	expectTaskStatusChange(mock, 42, 0, 1, nil, nil, 42, 77)
	if _, err := s.SetTaskStatus(taskListContext(), statusRequest(1)); err != nil {
		t.Fatal(err)
	}
	first := taskNotificationOutboxRow{NotificationID: 1, TeamID: 200, RecipientID: 42, EventVersion: 1}
	second := taskNotificationOutboxRow{NotificationID: 2, TeamID: 200, RecipientID: 77, EventVersion: 1}
	expectPending := func() {
		mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 100).
			WillReturnRows(outboxRows().AddRow(1, 200, 42, 1, false).AddRow(2, 200, 77, 1, false))
	}
	var writes []kafka.Message
	failAck := true
	writer := &taskNotificationPublisherWriterFake{write: func(ctx context.Context, messages ...kafka.Message) error {
		if ctx.Err() != nil || len(messages) != 1 {
			t.Fatal("invalid write context/count")
		}
		writes = append(writes, copyPublisherMessage(messages[0]))
		// SQL listing completed before the first network call; no mark starts before ACK.
		if failAck {
			failAck = false
			return errors.New("uncertain broker ACK")
		}
		return nil
	}}
	newPublisher := func() *taskNotificationPublisher {
		return newTaskNotificationPublisher(newTaskNotificationOutboxStore(s.db), writer, nil)
	}
	expectPending()
	if err := newPublisher().RunOnce(context.Background()); err == nil {
		t.Fatal("uncertain ACK claimed publication")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Reconstruct both adapter and loop: ACK succeeds but persisted marking fails.
	expectPending()
	expectOutboxLockedRow(mock, first)
	mock.ExpectExec(regexp.QuoteMeta(outboxPublishSQL)).WithArgs(true, int64(1), int64(200), int64(42), 1, false).
		WillReturnError(errors.New("storage temporarily unavailable"))
	mock.ExpectRollback()
	if err := newPublisher().RunOnce(context.Background()); err == nil {
		t.Fatal("failed marking claimed publication")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Next reconstruction republishes exactly the same first event then the second.
	expectPending()
	for _, row := range []taskNotificationOutboxRow{first, second} {
		expectOutboxLockedRow(mock, row)
		mock.ExpectExec(regexp.QuoteMeta(outboxPublishSQL)).WithArgs(true, row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, false).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	if err := newPublisher().RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 4 {
		t.Fatalf("unexpected attempts: %d", len(writes))
	}
	for i := 1; i < 3; i++ {
		if !bytes.Equal(writes[0].Key, writes[i].Key) || !bytes.Equal(writes[0].Value, writes[i].Value) {
			t.Fatal("reconstruction changed original event")
		}
	}
	if string(writes[3].Key) != second.Event().Key() {
		t.Fatal("second recipient missing or misrouted")
	}
	mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 100).WillReturnRows(outboxRows())
	if err := newPublisher().RunOnce(context.Background()); err != nil || len(writes) != 4 {
		t.Fatal("published records reappeared without pending rows")
	}
}
