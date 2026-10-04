package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type agentTriggerStoreStub struct {
	list func(context.Context, int) ([]model.AgentTriggerOutbox, error)
	mark func(context.Context, model.AgentTriggerOutbox) error
}

func (s *agentTriggerStoreStub) ListPending(ctx context.Context, limit int) ([]model.AgentTriggerOutbox, error) {
	if s.list == nil {
		return nil, nil
	}
	return s.list(ctx, limit)
}

func (s *agentTriggerStoreStub) MarkPublished(ctx context.Context, row model.AgentTriggerOutbox) error {
	if s.mark == nil {
		return nil
	}
	return s.mark(ctx, row)
}

type agentTriggerWriterStub struct {
	write      func(context.Context, ...kafka.Message) error
	closeCalls int
}

func (w *agentTriggerWriterStub) WriteMessages(ctx context.Context, messages ...kafka.Message) error {
	if w.write == nil {
		return nil
	}
	return w.write(ctx, messages...)
}

func (w *agentTriggerWriterStub) Close() error { w.closeCalls++; return nil }

func agentTriggerPublisherRow() model.AgentTriggerOutbox {
	return model.AgentTriggerOutbox{MessageID: 9007199254740993, EventVersion: model.AgentTriggerVersion,
		Action: model.AgentTriggerAction, MsgID: "original-client-message", ActorID: 501, TeamID: 200, GroupID: 300,
		Instruction: "private instruction: tomorrow fix the cache", ReferenceTimeMS: 1791000000000}
}

func TestAgentTriggerPublisherSendsOnlyStableNotificationThenMarksPublished(t *testing.T) {
	row := agentTriggerPublisherRow()
	var events []string
	var writeCtx context.Context
	store := &agentTriggerStoreStub{
		list: func(ctx context.Context, limit int) ([]model.AgentTriggerOutbox, error) {
			events = append(events, "list")
			deadline, ok := ctx.Deadline()
			if limit != 100 || !ok || time.Until(deadline) > 10*time.Second {
				t.Errorf("unbounded pending query: limit=%d deadline=%v", limit, deadline)
			}
			return []model.AgentTriggerOutbox{row}, nil
		},
		mark: func(ctx context.Context, got model.AgentTriggerOutbox) error {
			events = append(events, "mark")
			if got != row || ctx.Err() != nil || writeCtx.Err() != context.Canceled {
				t.Errorf("wrong fixed row or phase context: %+v", got)
			}
			return nil
		},
	}
	writer := &agentTriggerWriterStub{write: func(ctx context.Context, messages ...kafka.Message) error {
		events = append(events, "write")
		writeCtx = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || len(messages) != 1 {
			t.Fatalf("unbounded/batched write: %d,%v", len(messages), deadline)
		}
		message := messages[0]
		if string(message.Key) != "agent-trigger:tasks:9007199254740993" || string(message.Value) != `{"version":1,"action":"extract_tasks","message_id":"9007199254740993"}` || len(message.Headers) != 0 {
			t.Fatalf("wrong minimal event: %s %s %+v", message.Key, message.Value, message.Headers)
		}
		var body map[string]any
		if err := json.Unmarshal(message.Value, &body); err != nil || len(body) != 3 || strings.Contains(string(message.Value), row.Instruction) || strings.Contains(string(message.Value), row.MsgID) {
			t.Fatal("notification leaked source facts")
		}
		return nil
	}}
	if err := NewAgentTriggerPublisher(store, writer, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"list", "write", "mark"}) || writer.closeCalls != 0 {
		t.Fatalf("publication order: %v, closes=%d", events, writer.closeCalls)
	}
}

func TestAgentTriggerPublisherFailuresNeverInventPublicationOrLeakDetails(t *testing.T) {
	for _, phase := range []string{"list", "write", "mark"} {
		t.Run(phase, func(t *testing.T) {
			privateErr := errors.New("private instruction and user-token must stay hidden")
			row := agentTriggerPublisherRow()
			writes, marks := 0, 0
			store := &agentTriggerStoreStub{
				list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) {
					if phase == "list" {
						return nil, privateErr
					}
					return []model.AgentTriggerOutbox{row}, nil
				},
				mark: func(context.Context, model.AgentTriggerOutbox) error { marks++; return privateErr },
			}
			writer := &agentTriggerWriterStub{write: func(context.Context, ...kafka.Message) error {
				writes++
				if phase == "write" {
					return privateErr
				}
				return nil
			}}
			err := NewAgentTriggerPublisher(store, writer, zap.NewNop()).RunOnce(context.Background())
			if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "user-token") || row.Published ||
				(phase == "list" && writes != 0) || (phase != "mark" && marks != 0) || (phase == "mark" && marks != 1) {
				t.Fatalf("failure became publication/leak: %v, writes=%d marks=%d", err, writes, marks)
			}
		})
	}
}

func TestAgentTriggerPublisherRetriesSameKeyAndBytesBeyondTwoAttempts(t *testing.T) {
	for _, phase := range []string{"write", "mark"} {
		t.Run(phase, func(t *testing.T) {
			row := agentTriggerPublisherRow()
			var sent []kafka.Message
			marks := 0
			store := &agentTriggerStoreStub{
				list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) {
					if row.Published {
						return nil, nil
					}
					return []model.AgentTriggerOutbox{row}, nil
				},
				mark: func(_ context.Context, got model.AgentTriggerOutbox) error {
					marks++
					if got != row {
						t.Error("retry changed persisted facts")
					}
					if phase == "mark" && marks <= 3 {
						return errors.New("acknowledgement save lost")
					}
					row.Published = true
					return nil
				},
			}
			writer := &agentTriggerWriterStub{write: func(_ context.Context, messages ...kafka.Message) error {
				sent = append(sent, messages...)
				if phase == "write" && len(sent) <= 3 {
					return errors.New("broker response lost")
				}
				return nil
			}}
			for attempt := 0; attempt < 4; attempt++ {
				// Reconstructed publishers use only the retained Outbox facts.
				err := NewAgentTriggerPublisher(store, writer, nil).RunOnce(context.Background())
				if (attempt < 3) != (err != nil) || row.Published != (attempt == 3) {
					t.Fatalf("attempt %d: %v, published=%v", attempt, err, row.Published)
				}
			}
			if len(sent) != 4 {
				t.Fatalf("wrong retry count: %d", len(sent))
			}
			for _, message := range sent[1:] {
				if !bytes.Equal(message.Key, sent[0].Key) || !bytes.Equal(message.Value, sent[0].Value) {
					t.Fatal("retry changed event identity/body")
				}
			}
			if err := NewAgentTriggerPublisher(store, writer, nil).RunOnce(context.Background()); err != nil || len(sent) != 4 {
				t.Fatalf("published item resent: %v", err)
			}
		})
	}
}

func TestAgentTriggerPublisherRejectsInvalidRowsAndOversizedPendingBatch(t *testing.T) {
	for _, change := range []string{"message", "version", "action", "client ID", "actor", "team", "group", "reference", "instruction", "published", "oversized"} {
		t.Run(change, func(t *testing.T) {
			row := agentTriggerPublisherRow()
			switch change {
			case "message":
				row.MessageID = 0
			case "version":
				row.EventVersion++
			case "action":
				row.Action = "other"
			case "client ID":
				row.MsgID = ""
			case "actor":
				row.ActorID = 0
			case "team":
				row.TeamID = 0
			case "group":
				row.GroupID = 0
			case "reference":
				row.ReferenceTimeMS = 0
			case "instruction":
				row.Instruction = " padded private instruction "
			case "published":
				row.Published = true
			}
			rows := []model.AgentTriggerOutbox{row}
			if change == "oversized" {
				rows = make([]model.AgentTriggerOutbox, 101)
				for i := range rows {
					rows[i] = row
				}
			}
			writes, marks := 0, 0
			store := &agentTriggerStoreStub{list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) { return rows, nil },
				mark: func(context.Context, model.AgentTriggerOutbox) error { marks++; return nil }}
			writer := &agentTriggerWriterStub{write: func(context.Context, ...kafka.Message) error { writes++; return nil }}
			err := NewAgentTriggerPublisher(store, writer, nil).RunOnce(context.Background())
			if err == nil || writes != 0 || marks != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid pending result published: %v writes=%d marks=%d", err, writes, marks)
			}
		})
	}
}

func TestAgentTriggerPublisherRetainsUnpublishedRemainderAfterPartialSuccess(t *testing.T) {
	first, second := agentTriggerPublisherRow(), agentTriggerPublisherRow()
	second.MessageID++
	rows := []model.AgentTriggerOutbox{first, second}
	var sent []kafka.Message
	store := &agentTriggerStoreStub{
		list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) {
			var pending []model.AgentTriggerOutbox
			for _, row := range rows {
				if !row.Published {
					pending = append(pending, row)
				}
			}
			return pending, nil
		},
		mark: func(_ context.Context, got model.AgentTriggerOutbox) error {
			for i := range rows {
				if rows[i] == got {
					rows[i].Published = true
					return nil
				}
			}
			return errors.New("changed row")
		},
	}
	writer := &agentTriggerWriterStub{write: func(_ context.Context, messages ...kafka.Message) error {
		sent = append(sent, messages...)
		if len(sent) == 2 {
			return errors.New("second event uncertain")
		}
		return nil
	}}
	p := NewAgentTriggerPublisher(store, writer, nil)
	if err := p.RunOnce(context.Background()); err == nil || !rows[0].Published || rows[1].Published {
		t.Fatalf("partial result: %v %+v", err, rows)
	}
	if err := p.RunOnce(context.Background()); err != nil || !rows[1].Published || len(sent) != 3 ||
		!bytes.Equal(sent[1].Key, sent[2].Key) || !bytes.Equal(sent[1].Value, sent[2].Value) || bytes.Equal(sent[0].Key, sent[1].Key) {
		t.Fatalf("remaining retry changed event: %v %+v", err, rows)
	}
}

func TestAgentTriggerPublisherCancellationStopsBetweenEveryPhase(t *testing.T) {
	for _, phase := range []string{"before list", "during list", "during write", "during mark"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			row := agentTriggerPublisherRow()
			lists, writes, marks := 0, 0, 0
			if phase == "before list" {
				cancel()
			}
			store := &agentTriggerStoreStub{
				list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) {
					lists++
					if phase == "during list" {
						cancel()
					}
					return []model.AgentTriggerOutbox{row, row}, nil
				},
				mark: func(context.Context, model.AgentTriggerOutbox) error {
					marks++
					if phase == "during mark" {
						cancel()
					}
					return nil
				},
			}
			writer := &agentTriggerWriterStub{write: func(context.Context, ...kafka.Message) error {
				writes++
				if phase == "during write" {
					cancel()
				}
				return nil
			}}
			err := NewAgentTriggerPublisher(store, writer, nil).RunOnce(ctx)
			if !errors.Is(err, context.Canceled) || (phase == "before list" && lists != 0) ||
				(phase != "during mark" && marks != 0) || (phase == "during mark" && (marks != 1 || writes != 1)) ||
				(phase == "during list" && writes != 0) {
				t.Fatalf("cancellation ignored: %v list=%d write=%d mark=%d", err, lists, writes, marks)
			}
		})
	}
}

func TestAgentTriggerPublisherOwnTimeoutBoundsReadWriteAndPublishedUpdate(t *testing.T) {
	for _, phase := range []string{"list", "write", "mark"} {
		t.Run(phase, func(t *testing.T) {
			row := agentTriggerPublisherRow()
			marks := 0
			store := &agentTriggerStoreStub{
				list: func(ctx context.Context, _ int) ([]model.AgentTriggerOutbox, error) {
					if phase == "list" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return []model.AgentTriggerOutbox{row}, nil
				},
				mark: func(ctx context.Context, _ model.AgentTriggerOutbox) error {
					marks++
					if phase == "mark" {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				},
			}
			writer := &agentTriggerWriterStub{write: func(ctx context.Context, _ ...kafka.Message) error {
				if phase == "write" {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}}
			p := NewAgentTriggerPublisher(store, writer, nil)
			// Shorten only the test clock; production defaults are checked below.
			p.roundTimeout = 20 * time.Millisecond
			p.writeTimeout = 5 * time.Millisecond
			if err := p.RunOnce(context.Background()); !errors.Is(err, context.DeadlineExceeded) || (phase != "mark" && marks != 0) {
				t.Fatalf("unbounded phase %s: %v marks=%d", phase, err, marks)
			}
		})
	}
}

func TestAgentTriggerKafkaWriterConfigurationAndPublisherDefaults(t *testing.T) {
	writer := NewKafkaAgentTriggerWriter([]string{"broker:9092"}, "agent-triggers")
	kafkaWriter, ok := writer.(*kafka.Writer)
	if !ok || kafkaWriter.Async || kafkaWriter.RequiredAcks != kafka.RequireAll || kafkaWriter.Topic != "agent-triggers" ||
		kafkaWriter.MaxAttempts != 1 || kafkaWriter.WriteTimeout != 5*time.Second || kafkaWriter.ReadTimeout != 5*time.Second {
		t.Fatalf("not a synchronous bounded RequireAll writer: %+v", writer)
	}
	if _, ok := kafkaWriter.Balancer.(*kafka.Hash); !ok {
		t.Fatal("message key does not select a stable partition")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	p := NewAgentTriggerPublisher(nil, nil, nil)
	if p.roundTimeout != 10*time.Second || p.writeTimeout != 5*time.Second || p.pollInterval != time.Second || p.logger == nil {
		t.Fatal("missing publication budgets/default polling interval")
	}
	if err := p.RunOnce(context.Background()); err == nil {
		t.Fatal("unconfigured publisher succeeded")
	}
}

func TestAgentTriggerPublisherStartCancellationInterruptsPollingWithoutClosingWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listed := make(chan struct{})
	lists := 0
	store := &agentTriggerStoreStub{list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) {
		lists++
		if lists == 1 {
			close(listed)
		}
		return nil, nil
	}}
	writer := &agentTriggerWriterStub{}
	p := NewAgentTriggerPublisher(store, writer, nil)
	p.pollInterval = time.Hour
	done := make(chan struct{})
	go func() { p.Start(ctx); close(done) }()
	select {
	case <-listed:
	case <-time.After(time.Second):
		t.Fatal("publisher did not run its first round immediately")
	}
	select {
	case <-done:
		t.Fatal("publisher stopped without cancellation")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher cancellation waited for the polling interval")
	}
	if lists != 1 || writer.closeCalls != 0 {
		t.Fatalf("polling/ownership changed: lists=%d closes=%d", lists, writer.closeCalls)
	}
	if err := writer.Close(); err != nil || writer.closeCalls != 1 {
		t.Fatal("process could not close writer after publisher stopped")
	}
}

func TestAgentTriggerPublisherStartRetriesSafelyAndHonorsPreCanceledContext(t *testing.T) {
	row := agentTriggerPublisherRow()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lists, writes, marks := 0, 0, 0
	store := &agentTriggerStoreStub{
		list: func(context.Context, int) ([]model.AgentTriggerOutbox, error) {
			lists++
			if lists <= 3 {
				return nil, errors.New("user-token private instruction: original-client-message")
			}
			return []model.AgentTriggerOutbox{row}, nil
		},
		mark: func(context.Context, model.AgentTriggerOutbox) error { marks++; cancel(); return nil },
	}
	writer := &agentTriggerWriterStub{write: func(context.Context, ...kafka.Message) error { writes++; return nil }}
	core, observed := observer.New(zap.InfoLevel)
	p := NewAgentTriggerPublisher(store, writer, zap.New(core))
	p.pollInterval = time.Millisecond
	p.Start(ctx)
	if lists != 4 || writes != 1 || marks != 1 || writer.closeCalls != 0 {
		t.Fatalf("publisher did not retry retained event: list=%d write=%d mark=%d", lists, writes, marks)
	}
	if observed.FilterMessage("agent trigger publication failed; pending events remain retryable").Len() != 3 {
		t.Fatal("failed rounds were not logged")
	}
	for _, entry := range observed.All() {
		text := entry.Message + fmt.Sprint(entry.ContextMap())
		if strings.Contains(text, "user-token") || strings.Contains(text, "private instruction") || strings.Contains(text, row.MsgID) {
			t.Fatalf("publication logs leaked source details: %s", text)
		}
	}
	before := lists
	p.Start(ctx)
	if lists != before || writer.closeCalls != 0 {
		t.Fatal("pre-canceled start accessed store or closed writer")
	}
}
