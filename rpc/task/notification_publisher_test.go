package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type taskNotificationPublisherStoreFake struct {
	list func(context.Context, int) ([]taskNotificationOutboxRow, error)
	mark func(context.Context, taskNotificationOutboxRow) error
}

func (f *taskNotificationPublisherStoreFake) ListPending(ctx context.Context, limit int) ([]taskNotificationOutboxRow, error) {
	return f.list(ctx, limit)
}

func (f *taskNotificationPublisherStoreFake) MarkPublished(ctx context.Context, row taskNotificationOutboxRow) error {
	return f.mark(ctx, row)
}

type taskNotificationPublisherWriterFake struct {
	write      func(context.Context, ...kafka.Message) error
	closeCalls atomic.Int32
}

func (f *taskNotificationPublisherWriterFake) WriteMessages(ctx context.Context, messages ...kafka.Message) error {
	return f.write(ctx, messages...)
}

func (f *taskNotificationPublisherWriterFake) Close() error {
	f.closeCalls.Add(1)
	return nil
}

func publisherRow(id int64) taskNotificationOutboxRow {
	return taskNotificationOutboxRow{
		NotificationID: id,
		TeamID:         9007199254740993,
		RecipientID:    9007199254740995,
		EventVersion:   model.TaskNotificationEventVersion,
	}
}

func copyPublisherMessage(message kafka.Message) kafka.Message {
	return kafka.Message{Key: bytes.Clone(message.Key), Value: bytes.Clone(message.Value)}
}

func TestTaskNotificationPublisherSendsInOrderAndMarksAfterAck(t *testing.T) {
	rows := []taskNotificationOutboxRow{publisherRow(9007199254740997), publisherRow(9007199254740999)}
	var events []string
	store := &taskNotificationPublisherStoreFake{
		list: func(ctx context.Context, limit int) ([]taskNotificationOutboxRow, error) {
			if ctx.Err() != nil || limit != 100 {
				t.Fatalf("invalid list request: limit=%d, err=%v", limit, ctx.Err())
			}
			return rows, nil
		},
		mark: func(ctx context.Context, row taskNotificationOutboxRow) error {
			if ctx.Err() != nil {
				t.Fatalf("mark after context ended: %v", ctx.Err())
			}
			events = append(events, "mark:"+row.Event().Key())
			return nil
		},
	}
	writer := &taskNotificationPublisherWriterFake{write: func(ctx context.Context, messages ...kafka.Message) error {
		if ctx.Err() != nil || len(messages) != 1 {
			t.Fatalf("invalid synchronous write: count=%d, err=%v", len(messages), ctx.Err())
		}
		var event model.TaskNotificationEvent
		if err := json.Unmarshal(messages[0].Value, &event); err != nil || event.Validate() != nil || string(messages[0].Key) != event.Key() {
			t.Fatalf("wrong event payload/key: %s %s, err=%v", messages[0].Key, messages[0].Value, err)
		}
		events = append(events, "write:"+event.Key())
		return nil
	}}
	if err := newTaskNotificationPublisher(store, writer, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"write:task-notification:9007199254740997", "mark:task-notification:9007199254740997",
		"write:task-notification:9007199254740999", "mark:task-notification:9007199254740999",
	}
	if len(events) != len(want) {
		t.Fatalf("wrong event count/order: %v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("wrong event order: %v", events)
		}
	}
	if writer.closeCalls.Load() != 0 {
		t.Fatal("publisher closed writer owned by runtime")
	}
}

func TestTaskNotificationPublisherRejectsWholeInvalidBatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []taskNotificationOutboxRow
	}{
		{"over limit", func() []taskNotificationOutboxRow {
			rows := make([]taskNotificationOutboxRow, 101)
			for i := range rows {
				rows[i] = publisherRow(int64(i + 1))
			}
			return rows
		}()},
		{"published tail", []taskNotificationOutboxRow{publisherRow(1), func() taskNotificationOutboxRow { r := publisherRow(2); r.Published = true; return r }()}},
		{"invalid tail", []taskNotificationOutboxRow{publisherRow(1), func() taskNotificationOutboxRow { r := publisherRow(2); r.EventVersion = 9; return r }()}},
		{"duplicate tail", []taskNotificationOutboxRow{publisherRow(1), publisherRow(1)}},
		{"descending tail", []taskNotificationOutboxRow{publisherRow(2), publisherRow(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &taskNotificationPublisherStoreFake{
				list: func(context.Context, int) ([]taskNotificationOutboxRow, error) { return tc.rows, nil },
				mark: func(context.Context, taskNotificationOutboxRow) error {
					t.Fatal("invalid batch was marked")
					return nil
				},
			}
			writer := &taskNotificationPublisherWriterFake{write: func(context.Context, ...kafka.Message) error {
				t.Fatal("invalid batch was partially sent")
				return nil
			}}
			if err := newTaskNotificationPublisher(store, writer, nil).RunOnce(context.Background()); !errors.Is(err, errTaskNotificationPublishBatch) {
				t.Fatalf("invalid batch: %v", err)
			}
		})
	}
}

func TestTaskNotificationPublisherListFailureDoesNotSend(t *testing.T) {
	store := &taskNotificationPublisherStoreFake{
		list: func(context.Context, int) ([]taskNotificationOutboxRow, error) {
			return nil, errors.New("private SQL query detail")
		},
		mark: func(context.Context, taskNotificationOutboxRow) error { t.Fatal("failed list was marked"); return nil },
	}
	writer := &taskNotificationPublisherWriterFake{write: func(context.Context, ...kafka.Message) error {
		t.Fatal("failed list was sent")
		return nil
	}}
	if err := newTaskNotificationPublisher(store, writer, nil).RunOnce(context.Background()); !errors.Is(err, errTaskNotificationPublishList) || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe list failure: %v", err)
	}
}

func TestTaskNotificationPublisherReplaysSameEventAfterUncertainWriteOrMark(t *testing.T) {
	for _, failedPhase := range []string{"write", "mark"} {
		t.Run(failedPhase, func(t *testing.T) {
			row := publisherRow(9007199254740997)
			marked := false
			markCalls := 0
			var messages []kafka.Message
			store := &taskNotificationPublisherStoreFake{
				list: func(context.Context, int) ([]taskNotificationOutboxRow, error) {
					if marked {
						return nil, nil
					}
					return []taskNotificationOutboxRow{row}, nil
				},
				mark: func(_ context.Context, got taskNotificationOutboxRow) error {
					markCalls++
					if got != row {
						t.Fatalf("changed outbox fact: %+v", got)
					}
					if failedPhase == "mark" && markCalls == 1 {
						return errors.New("private SQL detail")
					}
					marked = true
					return nil
				},
			}
			writer := &taskNotificationPublisherWriterFake{write: func(_ context.Context, batch ...kafka.Message) error {
				if len(batch) != 1 {
					t.Fatalf("expected one event per write, got %d", len(batch))
				}
				messages = append(messages, copyPublisherMessage(batch[0]))
				if failedPhase == "write" && len(messages) == 1 {
					return errors.New("private broker detail")
				}
				return nil
			}}
			publisher := newTaskNotificationPublisher(store, writer, nil)
			if err := publisher.RunOnce(context.Background()); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe first failure: %v", err)
			}
			if marked {
				t.Fatal("unconfirmed event was marked")
			}
			if err := publisher.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !marked || len(messages) != 2 || !bytes.Equal(messages[0].Key, messages[1].Key) || !bytes.Equal(messages[0].Value, messages[1].Value) ||
				string(messages[0].Key) != row.Event().Key() {
				t.Fatalf("retry changed event or omitted mark: marked=%v, messages=%v", marked, messages)
			}
		})
	}
}

func TestTaskNotificationPublisherFailureLogIdentifiesOutboxRow(t *testing.T) {
	row := publisherRow(9007199254740997)
	for _, tc := range []struct {
		phase  string
		cause  error
		wantID bool
	}{
		{"list", errTaskNotificationPublishList, false},
		{"write", errTaskNotificationPublishWrite, true},
		{"mark", errTaskNotificationPublishMark, true},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			store := &taskNotificationPublisherStoreFake{
				list: func(context.Context, int) ([]taskNotificationOutboxRow, error) {
					if tc.phase == "list" {
						return nil, errors.New("private SQL detail")
					}
					return []taskNotificationOutboxRow{row}, nil
				},
				mark: func(context.Context, taskNotificationOutboxRow) error {
					if tc.phase == "mark" {
						return errors.New("private SQL detail")
					}
					return nil
				},
			}
			writer := &taskNotificationPublisherWriterFake{write: func(context.Context, ...kafka.Message) error {
				if tc.phase == "write" {
					return errors.New("private broker detail")
				}
				return nil
			}}
			core, observed := observer.New(zap.WarnLevel)
			publisher := newTaskNotificationPublisher(store, writer, zap.New(core))
			publisher.interval = time.Hour
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { publisher.Start(ctx); close(done) }()
			deadline := time.After(time.Second)
			for observed.Len() == 0 {
				select {
				case <-deadline:
					cancel()
					<-done
					t.Fatal("failure was not logged")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			<-done
			logs := observed.All()
			if len(logs) != 1 || logs[0].Message != "task notification publish round failed" {
				t.Fatalf("unexpected publish logs: %v", logs)
			}
			fields := logs[0].ContextMap()
			if fields["phase"] != tc.cause.Error() {
				t.Fatalf("wrong safe phase: %v", fields)
			}
			if tc.wantID {
				if len(fields) != 2 || fields["notification_id"] != row.NotificationID {
					t.Fatalf("missing failing notification ID or unsafe field: %v", fields)
				}
			} else if len(fields) != 1 {
				t.Fatalf("list failure cannot identify a row: %v", fields)
			}
		})
	}
}

func TestTaskNotificationPublisherStopsOnCanceledOrExpiredContext(t *testing.T) {
	row := publisherRow(1)
	for _, tc := range []struct {
		name  string
		list  func(context.Context, int) ([]taskNotificationOutboxRow, error)
		write func(context.Context, ...kafka.Message) error
	}{
		{"canceled during list", func(ctx context.Context, _ int) ([]taskNotificationOutboxRow, error) {
			<-ctx.Done()
			return []taskNotificationOutboxRow{row}, nil
		}, nil},
		{"expired during write but writer says success", func(_ context.Context, _ int) ([]taskNotificationOutboxRow, error) {
			return []taskNotificationOutboxRow{row}, nil
		}, func(ctx context.Context, _ ...kafka.Message) error {
			<-ctx.Done()
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &taskNotificationPublisherStoreFake{
				list: tc.list,
				mark: func(context.Context, taskNotificationOutboxRow) error {
					t.Fatal("expired event was marked")
					return nil
				},
			}
			writer := &taskNotificationPublisherWriterFake{write: tc.write}
			publisher := newTaskNotificationPublisher(store, writer, nil)
			publisher.roundTimeout = 40 * time.Millisecond
			publisher.writeTimeout = 20 * time.Millisecond
			if err := publisher.RunOnce(context.Background()); !errors.Is(err, errTaskNotificationPublishContext) {
				t.Fatalf("expired round: %v", err)
			}
		})
	}
}

func TestTaskNotificationPublisherCancellationAfterFakeWriteSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	row := publisherRow(1)
	store := &taskNotificationPublisherStoreFake{
		list: func(context.Context, int) ([]taskNotificationOutboxRow, error) {
			return []taskNotificationOutboxRow{row}, nil
		},
		mark: func(context.Context, taskNotificationOutboxRow) error {
			t.Fatal("canceled write was marked")
			return nil
		},
	}
	writer := &taskNotificationPublisherWriterFake{write: func(context.Context, ...kafka.Message) error {
		cancel()
		return nil
	}}
	if err := newTaskNotificationPublisher(store, writer, nil).RunOnce(ctx); !errors.Is(err, errTaskNotificationPublishContext) {
		t.Fatalf("fake success after cancellation: %v", err)
	}
}

func TestTaskNotificationPublisherMissingDependencyAndStartPause(t *testing.T) {
	if err := (*taskNotificationPublisher)(nil).RunOnce(context.Background()); !errors.Is(err, errTaskNotificationPublishUnavailable) {
		t.Fatalf("nil publisher: %v", err)
	}
	if err := newTaskNotificationPublisher(nil, nil, nil).RunOnce(context.Background()); !errors.Is(err, errTaskNotificationPublishUnavailable) {
		t.Fatalf("missing dependencies: %v", err)
	}
	calls := make(chan struct{}, 2)
	store := &taskNotificationPublisherStoreFake{
		list: func(context.Context, int) ([]taskNotificationOutboxRow, error) {
			select {
			case calls <- struct{}{}:
			default:
			}
			return nil, nil
		},
		mark: func(context.Context, taskNotificationOutboxRow) error { return nil },
	}
	writer := &taskNotificationPublisherWriterFake{write: func(context.Context, ...kafka.Message) error { return nil }}
	publisher := newTaskNotificationPublisher(store, writer, nil)
	publisher.interval = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { publisher.Start(ctx); close(done) }()
	select {
	case <-calls:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("publisher did not start")
	}
	select {
	case <-calls:
		cancel()
		t.Fatal("publisher spun without waiting for next round")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop on cancellation")
	}
	if writer.closeCalls.Load() != 0 {
		t.Fatal("publisher closed runtime-owned writer")
	}
}
