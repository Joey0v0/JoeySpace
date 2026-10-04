package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type triggerConsumerTestReader struct {
	fetch  func(context.Context) (kafka.Message, error)
	commit func(context.Context, ...kafka.Message) error
	close  func() error
}

func (r *triggerConsumerTestReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	if r.fetch != nil {
		return r.fetch(ctx)
	}
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (r *triggerConsumerTestReader) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	if r.commit != nil {
		return r.commit(ctx, messages...)
	}
	return nil
}

func (r *triggerConsumerTestReader) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

type triggerConsumerTestInbox func(context.Context, model.AgentTriggerEvent) error

func (f triggerConsumerTestInbox) Accept(ctx context.Context, event model.AgentTriggerEvent) error {
	return f(ctx, event)
}

func testTriggerConsumer(t *testing.T, reader TriggerEventReader, inbox TriggerInbox) *TriggerConsumer {
	t.Helper()
	c, err := NewTriggerConsumer(reader, inbox)
	if err != nil {
		t.Fatal(err)
	}
	c.retryDelay = time.Millisecond
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestTriggerConsumerRetriesWithoutPassingUncommittedOffset(t *testing.T) {
	first := notificationTestMessage(t, 9007199254740993)
	second := notificationTestMessage(t, 43)
	second.Offset = first.Offset + 1
	second.Value = []byte("bad notification")
	var sequence []string
	var fetches, accepts, commits int
	r := &triggerConsumerTestReader{}
	r.fetch = func(context.Context) (kafka.Message, error) {
		fetches++
		sequence = append(sequence, "fetch")
		switch fetches {
		case 1:
			return kafka.Message{}, errors.New("temporary private broker failure")
		case 2:
			return first, nil
		case 3:
			if accepts != 2 || commits != 2 {
				t.Fatal("fetched higher offset before current durable acknowledgement")
			}
			return second, nil
		default:
			t.Fatal("continued fetching after malformed notification")
			return kafka.Message{}, nil
		}
	}
	inbox := triggerConsumerTestInbox(func(_ context.Context, event model.AgentTriggerEvent) error {
		accepts++
		sequence = append(sequence, "accept")
		if event != (model.AgentTriggerEvent{Version: 1, Action: model.AgentTriggerAction, MessageID: 9007199254740993}) {
			t.Fatalf("saved different event during retry: %+v", event)
		}
		if accepts == 1 {
			return status.Error(codes.Unavailable, "private SQL password")
		}
		return nil
	})
	r.commit = func(ctx context.Context, messages ...kafka.Message) error {
		commits++
		sequence = append(sequence, "commit")
		if !reflect.DeepEqual(messages, []kafka.Message{first}) || accepts != 2 {
			t.Fatal("commit changed record or saved again after successful Accept")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("commit has no three-second budget")
		}
		if commits == 1 {
			return errors.New("private uncertain Kafka ACK")
		}
		return nil
	}
	c := testTriggerConsumer(t, r, inbox)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Run(ctx); err != ErrInvalidTriggerNotification {
		t.Fatalf("wrong terminal error: %v", err)
	}
	want := []string{"fetch", "fetch", "accept", "accept", "commit", "commit", "fetch"}
	if !reflect.DeepEqual(sequence, want) || fetches != 3 || accepts != 2 || commits != 2 {
		t.Fatalf("sequence=%v want=%v", sequence, want)
	}
}

func TestTriggerConsumerInvalidNotificationAndInboxStopWithoutAcknowledgement(t *testing.T) {
	for _, badInbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "bad notification", true: "bad persisted fact"}[badInbox], func(t *testing.T) {
			message := notificationTestMessage(t, 42)
			if !badInbox {
				message.Value = []byte(`{"version":1,"action":"extract_tasks","message_id":"42","actor_id":999}`)
			}
			var fetched, accepted, committed int
			r := &triggerConsumerTestReader{fetch: func(context.Context) (kafka.Message, error) {
				fetched++
				return message, nil
			}, commit: func(context.Context, ...kafka.Message) error { committed++; return nil }}
			inbox := triggerConsumerTestInbox(func(context.Context, model.AgentTriggerEvent) error {
				accepted++
				return status.Error(codes.FailedPrecondition, "secret corrupt SQL facts")
			})
			c := testTriggerConsumer(t, r, inbox)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			want := ErrInvalidTriggerNotification
			wantAccept := 0
			if badInbox {
				want, wantAccept = ErrInvalidTriggerInbox, 1
			}
			err := c.Run(ctx)
			if err != want || fetched != 1 || accepted != wantAccept || committed != 0 {
				t.Fatalf("err=%v fetch=%d accept=%d commit=%d", err, fetched, accepted, committed)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "SQL") {
				t.Fatal("terminal error leaked underlying details")
			}
		})
	}
}

func TestTriggerConsumerCancellationWinsAtEveryStage(t *testing.T) {
	for _, stage := range []string{"before fetch", "fetch", "accept", "commit"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			message := notificationTestMessage(t, 42)
			var fetches, accepts, commits int
			r := &triggerConsumerTestReader{}
			r.fetch = func(context.Context) (kafka.Message, error) {
				fetches++
				if stage == "fetch" {
					cancel()
					return kafka.Message{Value: []byte("malformed")}, nil
				}
				return message, nil
			}
			inbox := triggerConsumerTestInbox(func(context.Context, model.AgentTriggerEvent) error {
				accepts++
				if stage == "accept" {
					cancel()
					return status.Error(codes.FailedPrecondition, "private invalid record")
				}
				return nil
			})
			r.commit = func(context.Context, ...kafka.Message) error {
				commits++
				cancel()
				return errors.New("private commit error")
			}
			c := testTriggerConsumer(t, r, inbox)
			if stage == "before fetch" {
				cancel()
			}
			if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost to stage error: %v", err)
			}
			if stage == "before fetch" && fetches != 0 || stage == "fetch" && (accepts != 0 || commits != 0) ||
				stage == "accept" && commits != 0 || stage == "commit" && (fetches != 1 || accepts != 1 || commits != 1) {
				t.Fatalf("continued after cancellation: fetch=%d accept=%d commit=%d", fetches, accepts, commits)
			}
		})
	}
}

func TestTriggerConsumerRetryWaitsAndBlockedCommitRespectCallerDeadline(t *testing.T) {
	for _, stage := range []string{"fetch", "accept", "commit", "blocked commit"} {
		t.Run(stage, func(t *testing.T) {
			message := notificationTestMessage(t, 42)
			var fetches, accepts, commits int
			r := &triggerConsumerTestReader{fetch: func(context.Context) (kafka.Message, error) {
				fetches++
				if stage == "fetch" {
					return kafka.Message{}, errors.New("temporary fetch error")
				}
				return message, nil
			}}
			inbox := triggerConsumerTestInbox(func(context.Context, model.AgentTriggerEvent) error {
				accepts++
				if stage == "accept" {
					return status.Error(codes.Unavailable, "temporary SQL error")
				}
				return nil
			})
			r.commit = func(ctx context.Context, _ ...kafka.Message) error {
				commits++
				if stage == "blocked commit" {
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("temporary acknowledgement error")
			}
			c := testTriggerConsumer(t, r, inbox)
			c.retryDelay = time.Hour
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := c.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("caller deadline not preserved", err)
			}
			if fetches != 1 || accepts > 1 || commits > 1 || stage == "fetch" && (accepts != 0 || commits != 0) ||
				stage == "accept" && commits != 0 || stage == "blocked commit" && commits != 1 {
				t.Fatalf("retried/passed record while deadline expired: fetch=%d accept=%d commit=%d", fetches, accepts, commits)
			}
		})
	}
}

func TestTriggerConsumerConstructorCloseAndConfigurationGuards(t *testing.T) {
	reader := &triggerConsumerTestReader{}
	inbox := triggerConsumerTestInbox(func(context.Context, model.AgentTriggerEvent) error { return nil })
	for _, tc := range []struct {
		reader TriggerEventReader
		inbox  TriggerInbox
	}{
		{nil, inbox}, {reader, nil}, {(*triggerConsumerTestReader)(nil), inbox}, {reader, triggerConsumerTestInbox(nil)},
	} {
		if c, err := NewTriggerConsumer(tc.reader, tc.inbox); err == nil || c != nil {
			t.Fatal("constructor accepted missing dependency")
		}
	}
	var nilConsumer *TriggerConsumer
	if err := nilConsumer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := nilConsumer.Run(context.Background()); err == nil {
		t.Fatal("nil consumer accepted Run")
	}
	var closes atomic.Int32
	reader.close = func() error { closes.Add(1); return errors.New("secret broker credentials") }
	c, err := NewTriggerConsumer(reader, inbox)
	if err != nil || c.retryDelay != time.Second {
		t.Fatalf("production constructor defaults: %v %v", c, err)
	}
	if err := c.Run(nil); err == nil {
		t.Fatal("nil context accepted")
	}
	var group sync.WaitGroup
	for range 5 {
		group.Add(1)
		go func() {
			defer group.Done()
			err := c.Close()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Error("Close lost or leaked error", err)
			}
		}()
	}
	group.Wait()
	if closes.Load() != 1 || c.Close() != c.closeErr {
		t.Fatal("Close is not idempotent")
	}
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("closed consumer accepted Run")
	}
}

func TestTriggerConsumerCannotRunConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	reader := &triggerConsumerTestReader{fetch: func(ctx context.Context) (kafka.Message, error) {
		close(started)
		<-ctx.Done()
		return kafka.Message{}, ctx.Err()
	}}
	c := testTriggerConsumer(t, reader, triggerConsumerTestInbox(func(context.Context, model.AgentTriggerEvent) error { return nil }))
	go func() { done <- c.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("consumer did not start")
	}
	if err := c.Run(context.Background()); err == nil {
		t.Error("second Run fetched concurrently")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
