package push

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type notificationReaderFake struct {
	fetch  func(context.Context) (kafka.Message, error)
	commit func(context.Context, ...kafka.Message) error
	closes int
}

func (r *notificationReaderFake) FetchMessage(ctx context.Context) (kafka.Message, error) {
	return r.fetch(ctx)
}
func (r *notificationReaderFake) CommitMessages(ctx context.Context, m ...kafka.Message) error {
	return r.commit(ctx, m...)
}
func (r *notificationReaderFake) Close() error { r.closes++; return nil }

type notificationOnlineFake func(context.Context, int64) (string, error)

func (f notificationOnlineFake) GetOnline(ctx context.Context, id int64) (string, error) {
	return f(ctx, id)
}

type notificationSenderFake func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error)

func (f notificationSenderFake) Send(ctx context.Context, addr string, e model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
	return f(ctx, addr, e)
}

func consumerNotificationMessage(t *testing.T) (model.TaskNotificationEvent, kafka.Message) {
	t.Helper()
	e := model.TaskNotificationEvent{Version: 1, Type: model.TaskNotificationEventType, NotificationID: 9007199254740997, TeamID: 200, RecipientID: 42}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return e, kafka.Message{Topic: "task_notifications", Partition: 0, Offset: 17, Key: []byte(e.Key()), Value: body}
}
func notificationTestConsumer(t *testing.T, r TaskNotificationReader, online TaskNotificationOnlineLocator, sender TaskNotificationSender) *TaskNotificationConsumer {
	t.Helper()
	c, err := NewTaskNotificationConsumer(r, online, sender, "task_notifications", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.retryDelay = time.Millisecond
	return c
}

func TestTaskNotificationConsumerTerminalOutcomesCommitWithoutChangingNotice(t *testing.T) {
	for _, outcome := range []string{model.TaskNotificationQueued, model.TaskNotificationOffline, model.TaskNotificationDenied, "redis_offline"} {
		t.Run(outcome, func(t *testing.T) {
			e, message := consumerNotificationMessage(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fetches, commits, sends := 0, 0, 0
			r := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) { fetches++; return message, nil }, commit: func(ctx context.Context, m ...kafka.Message) error {
				commits++
				if len(m) != 1 || m[0].Offset != 17 || m[0].Topic != message.Topic || ctx.Err() != nil {
					t.Fatal("wrong offset acknowledgement")
				}
				cancel()
				return nil
			}}
			online := notificationOnlineFake(func(ctx context.Context, id int64) (string, error) {
				if id != 42 {
					t.Fatal("wrong recipient")
				}
				if outcome == "redis_offline" {
					return "", nil
				}
				return "im-ws:9091", nil
			})
			sender := notificationSenderFake(func(ctx context.Context, addr string, event model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
				sends++
				if addr != "im-ws:9091" || event != e {
					t.Fatal("event changed")
				}
				return model.TaskNotificationDelivery{NotificationID: e.NotificationID, Outcome: outcome}, nil
			})
			c := notificationTestConsumer(t, r, online, sender)
			if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			wantSends := 1
			if outcome == "redis_offline" {
				wantSends = 0
			}
			if fetches != 1 || commits != 1 || sends != wantSends {
				t.Fatalf("fetch/commit/send=%d/%d/%d", fetches, commits, sends)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			if r.closes != 1 {
				t.Fatal("reader not closed once")
			}
		})
	}
}

func TestTaskNotificationConsumerTemporaryFailuresRetrySameEventThenOnlyCommit(t *testing.T) {
	e, message := consumerNotificationMessage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetches, lookups, sends, commits := 0, 0, 0, 0
	r := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) { fetches++; return message, nil }, commit: func(context.Context, ...kafka.Message) error {
		commits++
		if commits == 1 {
			return errors.New("uncertain commit")
		}
		cancel()
		return nil
	}}
	online := notificationOnlineFake(func(context.Context, int64) (string, error) {
		lookups++
		if lookups == 1 {
			return "", errors.New("redis unavailable")
		}
		return "im-ws:9091", nil
	})
	sender := notificationSenderFake(func(ctx context.Context, addr string, event model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
		sends++
		if event != e {
			t.Fatal("retry changed original event")
		}
		if sends == 1 {
			return model.TaskNotificationDelivery{}, errors.New("temporary network failure")
		}
		return model.TaskNotificationDelivery{NotificationID: e.NotificationID, Outcome: model.TaskNotificationQueued}, nil
	})
	core, observed := observer.New(zap.WarnLevel)
	c := notificationTestConsumer(t, r, online, sender)
	c.logger = zap.New(core)
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if fetches != 1 || lookups != 3 || sends != 2 || commits != 2 {
		t.Fatalf("fetch/lookup/send/commit=%d/%d/%d/%d", fetches, lookups, sends, commits)
	}
	logs := observed.All()
	if len(logs) != 3 {
		t.Fatalf("want two delivery retries and one commit retry, got %d logs", len(logs))
	}
	for i, log := range logs {
		fields := log.ContextMap()
		if fields["notification_id"] != e.NotificationID || fields["topic"] != message.Topic ||
			fields["partition"] != int64(message.Partition) || fields["offset"] != message.Offset {
			t.Fatalf("retry %d cannot locate event: %v", i, fields)
		}
		if _, has := fields["error"]; has {
			t.Fatal("raw dependency error logged")
		}
	}
	for i, want := range []string{"task notification online lookup failed", "task notification delivery attempt failed", "offset_commit"} {
		if logs[i].ContextMap()["phase"] != want {
			t.Fatalf("retry %d phase = %v, want %s", i, logs[i].ContextMap()["phase"], want)
		}
	}
}

func TestTaskNotificationConsumerInvalidEventLatchesWithoutCommitOrLaterFetch(t *testing.T) {
	_, base := consumerNotificationMessage(t)
	for _, tc := range []struct {
		name   string
		mutate func(*kafka.Message)
	}{
		{"wrong key", func(m *kafka.Message) { m.Key = []byte("other") }}, {"wrong topic", func(m *kafka.Message) { m.Topic = "chat_messages" }},
		{"bad partition", func(m *kafka.Message) { m.Partition = -1 }}, {"bad offset", func(m *kafka.Message) { m.Offset = -1 }},
		{"malformed", func(m *kafka.Message) { m.Value = []byte("private malformed") }},
		{"future version", func(m *kafka.Message) {
			m.Value = []byte(`{"version":2,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":"3"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := base
			tc.mutate(&message)
			fetches := 0
			r := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) { fetches++; return message, nil }, commit: func(context.Context, ...kafka.Message) error { t.Fatal("invalid event committed"); return nil }}
			c := notificationTestConsumer(t, r, notificationOnlineFake(func(context.Context, int64) (string, error) { t.Fatal("invalid event routed"); return "", nil }), notificationSenderFake(func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
				t.Fatal("invalid event sent")
				return model.TaskNotificationDelivery{}, nil
			}))
			core, observed := observer.New(zap.ErrorLevel)
			c.logger = zap.New(core)
			for i := 0; i < 2; i++ {
				if !errors.Is(c.Run(context.Background()), ErrInvalidTaskNotification) {
					t.Fatal("bad event did not halt")
				}
			}
			if fetches != 1 {
				t.Fatal("stopped consumer fetched later event")
			}
			logs := observed.All()
			if len(logs) != 1 {
				t.Fatalf("want one location-only rejection log, got %d", len(logs))
			}
			fields := logs[0].ContextMap()
			if fields["phase"] != "event_validation" || fields["topic"] != message.Topic ||
				fields["partition"] != int64(message.Partition) || fields["offset"] != message.Offset || len(fields) != 4 {
				t.Fatalf("rejection log contains wrong fields: %v", fields)
			}
		})
	}
}

func TestTaskNotificationConsumerExpiredIOSuccessDoesNotCommit(t *testing.T) {
	for _, during := range []string{"lookup", "send"} {
		t.Run(during, func(t *testing.T) {
			e, message := consumerNotificationMessage(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) { return message, nil }, commit: func(context.Context, ...kafka.Message) error { t.Fatal("cancelled attempt committed"); return nil }}
			online := notificationOnlineFake(func(context.Context, int64) (string, error) {
				if during == "lookup" {
					cancel()
					return "", nil
				}
				return "im-ws:9091", nil
			})
			sender := notificationSenderFake(func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
				cancel()
				return model.TaskNotificationDelivery{NotificationID: e.NotificationID, Outcome: model.TaskNotificationQueued}, nil
			})
			if err := notificationTestConsumer(t, r, online, sender).Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskNotificationConsumerBadDeliveryReplyIsRetriedNotAcknowledged(t *testing.T) {
	e, message := consumerNotificationMessage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	r := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) { return message, nil }, commit: func(context.Context, ...kafka.Message) error { t.Fatal("invalid reply committed"); return nil }}
	sender := notificationSenderFake(func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return model.TaskNotificationDelivery{NotificationID: e.NotificationID + 1, Outcome: model.TaskNotificationQueued}, nil
	})
	if err := notificationTestConsumer(t, r, notificationOnlineFake(func(context.Context, int64) (string, error) { return "im-ws:9091", nil }), sender).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatal("bad reply not retried")
	}
}

func TestTaskNotificationConsumerRejectsMissingDependenciesAndFetchCancellation(t *testing.T) {
	if _, err := NewTaskNotificationConsumer(nil, nil, nil, "task_notifications", nil); err == nil {
		t.Fatal("missing dependencies accepted")
	}
	var zero TaskNotificationConsumer
	if err := zero.Run(context.Background()); err == nil {
		t.Fatal("zero consumer ran")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) {
		cancel()
		return kafka.Message{}, errors.New("fetch interrupted")
	}, commit: func(context.Context, ...kafka.Message) error { t.Fatal("interrupted fetch committed"); return nil }}
	c := notificationTestConsumer(t, r, notificationOnlineFake(func(context.Context, int64) (string, error) { return "", nil }), notificationSenderFake(func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
		return model.TaskNotificationDelivery{}, nil
	}))
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
