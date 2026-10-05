package main

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/push"
	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type notificationDeliveryReader struct {
	fetch  func(context.Context) (kafka.Message, error)
	commit func(context.Context, ...kafka.Message) error
	closes int
}

func (r *notificationDeliveryReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	return r.fetch(ctx)
}
func (r *notificationDeliveryReader) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	return r.commit(ctx, messages...)
}
func (r *notificationDeliveryReader) Close() error { r.closes++; return nil }

type notificationDeliveryOnline func(context.Context, int64) (string, error)

func (f notificationDeliveryOnline) GetOnline(ctx context.Context, id int64) (string, error) {
	return f(ctx, id)
}

type notificationDeliverySender func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error)

func (f notificationDeliverySender) Send(ctx context.Context, addr string, event model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
	return f(ctx, addr, event)
}

func TestTaskNotificationDeliveryFlowReplaysPublisherOutputWithoutChangingNotice(t *testing.T) {
	for _, failure := range []string{"broker ACK uncertain", "outbox commit uncertain"} {
		t.Run(failure, func(t *testing.T) {
			flowCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			userCtx := metadata.NewIncomingContext(flowCtx, metadata.Pairs("authorization", "Bearer sample"))
			s, mock := statusTaskServer(t, 42, 0)
			expectTaskStatusRow(mock, 42, nil, 0)
			expectTaskStatusChange(mock, 42, 0, 1, nil, nil, 42)
			if _, err := s.SetTaskStatus(userCtx, statusRequest(1)); err != nil {
				t.Fatal(err)
			}
			readNotice := func() *pb.ListTaskNotificationsResponse {
				mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), 2).
					WillReturnRows(notificationRows().AddRow(1, 500, 42, 0, 1, 1790874000123, 0))
				result, err := s.ListTaskNotifications(userCtx, &pb.ListTaskNotificationsRequest{TeamId: 200, Limit: 1})
				if err != nil || result == nil || len(result.Notifications) != 1 || result.Notifications[0].NotificationId != 1 || result.Notifications[0].ReadAtUnixMs != 0 {
					t.Fatalf("original personal unread notice: %v, %v", result, err)
				}
				return result
			}
			before := readNotice()
			row := taskNotificationOutboxRow{NotificationID: 1, TeamID: 200, RecipientID: 42, EventVersion: 1}
			var writes []kafka.Message
			writer := &taskNotificationPublisherWriterFake{write: func(ctx context.Context, batch ...kafka.Message) error {
				if ctx.Err() != nil || len(batch) != 1 {
					t.Fatal("invalid publisher write context or batch")
				}
				writes = append(writes, copyPublisherMessage(batch[0]))
				if failure == "broker ACK uncertain" && len(writes) == 1 {
					// The broker may have accepted the event despite this lost ACK.
					return errors.New("broker acknowledgement lost")
				}
				return nil
			}}
			t.Cleanup(func() {
				if err := writer.Close(); err != nil || writer.closeCalls.Load() != 1 {
					t.Errorf("writer ownership: closes=%d err=%v", writer.closeCalls.Load(), err)
				}
			})
			for attempt := 0; attempt < 2; attempt++ {
				mock.ExpectQuery(regexp.QuoteMeta(outboxPendingSQL)).WithArgs(false, 100).
					WillReturnRows(outboxRows().AddRow(1, 200, 42, 1, false))
				if attempt == 1 || failure == "outbox commit uncertain" {
					expectOutboxLockedRow(mock, row)
					mock.ExpectExec(regexp.QuoteMeta(outboxPublishSQL)).WithArgs(true, int64(1), int64(200), int64(42), 1, false).
						WillReturnResult(sqlmock.NewResult(0, 1))
					commit := mock.ExpectCommit()
					if attempt == 0 {
						// Model the not-committed branch of an uncertain result: still pending on reconstruction.
						commit.WillReturnError(errors.New("outbox commit result lost"))
					}
				}
				publisher := newTaskNotificationPublisher(newTaskNotificationOutboxStore(s.db), writer, nil)
				err := publisher.RunOnce(flowCtx)
				wantFailure := errTaskNotificationPublishWrite
				if failure == "outbox commit uncertain" {
					wantFailure = errTaskNotificationPublishMark
				}
				if (attempt == 0 && !errors.Is(err, wantFailure)) || (attempt == 1 && err != nil) {
					t.Fatalf("reconstructed publication attempt %d: %v", attempt, err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			}
			if len(writes) != 2 || !bytes.Equal(writes[0].Key, writes[1].Key) || !bytes.Equal(writes[0].Value, writes[1].Value) {
				t.Fatalf("reconstruction changed or omitted publisher output: %+v", writes)
			}
			// Supply only broker metadata; do not decode/re-encode the producer's Key/Value.
			for i := range writes {
				writes[i].Topic, writes[i].Partition, writes[i].Offset = "task_notifications", 3, int64(81+i)
			}
			consumeCtx, cancel := context.WithCancel(flowCtx)
			defer cancel()
			fetches, sends, commits := 0, 0, 0
			reader := &notificationDeliveryReader{
				fetch: func(ctx context.Context) (kafka.Message, error) {
					if ctx.Err() != nil || fetches != commits || fetches >= len(writes) {
						t.Fatal("consumer advanced without committing the preceding event")
					}
					message := writes[fetches]
					fetches++
					return message, nil
				},
				commit: func(ctx context.Context, batch ...kafka.Message) error {
					if ctx.Err() != nil || len(batch) != 1 || sends != commits+1 {
						t.Fatal("consumer committed before the corresponding hint outcome")
					}
					want := writes[commits]
					got := batch[0]
					if got.Topic != want.Topic || got.Partition != want.Partition || got.Offset != want.Offset || !bytes.Equal(got.Key, want.Key) || !bytes.Equal(got.Value, want.Value) {
						t.Fatal("consumer did not acknowledge the exact delivered Kafka record")
					}
					commits++
					if commits == len(writes) {
						cancel()
					}
					return nil
				},
			}
			online := notificationDeliveryOnline(func(ctx context.Context, recipient int64) (string, error) {
				if ctx.Err() != nil || recipient != row.RecipientID {
					t.Fatal("publisher recipient was not preserved by consumer routing")
				}
				return "im-ws:9091", nil
			})
			sender := notificationDeliverySender(func(ctx context.Context, addr string, event model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
				if ctx.Err() != nil || addr != "im-ws:9091" || event != row.Event() {
					t.Fatal("publisher facts changed before online delivery")
				}
				sends++
				return model.TaskNotificationDelivery{NotificationID: event.NotificationID, Outcome: model.TaskNotificationQueued}, nil
			})
			consumer, err := push.NewTaskNotificationConsumer(reader, online, sender, "task_notifications", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := consumer.Close(); err != nil || reader.closes != 1 {
					t.Errorf("reader ownership: closes=%d err=%v", reader.closes, err)
				}
			})
			// Run synchronously: callbacks can fail the test safely, and cancellation is awaited by return.
			if err := consumer.Run(consumeCtx); !errors.Is(err, context.Canceled) {
				t.Fatalf("consumer did not stop after both exact offsets: %v", err)
			}
			if fetches != 2 || sends != 2 || commits != 2 {
				t.Fatalf("at-least-once delivery counts: fetch=%d send=%d commit=%d", fetches, sends, commits)
			}
			if after := readNotice(); !proto.Equal(before, after) {
				t.Fatalf("hint replay changed personal notice: before=%v after=%v", before, after)
			}
			// No notification INSERT or read UPDATE is permitted after the status transaction.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
