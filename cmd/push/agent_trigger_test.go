package main

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/push"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func validTriggerKafkaConfig() config.KafkaConfig {
	return config.KafkaConfig{AgentTriggerEnabled: true, Brokers: []string{"kafka:19092"}, TopicChat: "chat_messages", TopicAgentTrigger: "agent_task_triggers"}
}

type pushRunnerFunc func(context.Context)

func (f pushRunnerFunc) Start(ctx context.Context) { f(ctx) }

type pushCloseFunc func() error

func (f pushCloseFunc) Close() error { return f() }

type runtimeConsumer struct {
	start func(context.Context)
	close func() error
}

func (c runtimeConsumer) Start(ctx context.Context) { c.start(ctx) }
func (c runtimeConsumer) Close() error              { return c.close() }

type runtimeMessages struct {
	repository.MessageRepository
	creates atomic.Int32
}

func (m *runtimeMessages) Create(context.Context, *model.Message) error {
	m.creates.Add(1)
	return nil
}

func awaitRuntimeSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach the expected lifecycle state")
	}
}

func TestAgentTriggerConfigRejectsAmbiguousTopicsAndMissingBrokersBeforeFactories(t *testing.T) {
	for name, change := range map[string]func(*config.KafkaConfig){
		"no brokers":       func(c *config.KafkaConfig) { c.Brokers = nil },
		"empty broker":     func(c *config.KafkaConfig) { c.Brokers = []string{""} },
		"blank second":     func(c *config.KafkaConfig) { c.Brokers = []string{"kafka:19092", " \t\r\n"} },
		"unicode broker":   func(c *config.KafkaConfig) { c.Brokers = []string{"\u2003"} },
		"empty trigger":    func(c *config.KafkaConfig) { c.TopicAgentTrigger = "" },
		"blank trigger":    func(c *config.KafkaConfig) { c.TopicAgentTrigger = " \t" },
		"leading trigger":  func(c *config.KafkaConfig) { c.TopicAgentTrigger = " agent_task_triggers" },
		"trailing trigger": func(c *config.KafkaConfig) { c.TopicAgentTrigger = "agent_task_triggers\n" },
		"internal trigger": func(c *config.KafkaConfig) { c.TopicAgentTrigger = "agent task" },
		"unicode trigger":  func(c *config.KafkaConfig) { c.TopicAgentTrigger = "agent\u00a0task" },
		"empty chat":       func(c *config.KafkaConfig) { c.TopicChat = "" },
		"leading chat":     func(c *config.KafkaConfig) { c.TopicChat = " chat_messages" },
		"trailing chat":    func(c *config.KafkaConfig) { c.TopicChat = "chat_messages\t" },
		"unicode chat":     func(c *config.KafkaConfig) { c.TopicChat = "chat\u2003messages" },
		"same topic":       func(c *config.KafkaConfig) { c.TopicAgentTrigger = c.TopicChat },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validTriggerKafkaConfig()
			change(&cfg)
			factories := agentTriggerFactories{
				legacy: func() repository.MessageRepository {
					t.Fatal("invalid enabled config reached legacy factory")
					return nil
				},
				enabled: func(*gorm.DB, []string, string, *zap.Logger) pushMessaging {
					t.Fatal("invalid config reached Outbox resources")
					return pushMessaging{}
				},
			}
			if _, err := newPushMessaging(cfg, nil, zap.NewNop(), factories); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	if err := validateAgentTriggerConfig(validTriggerKafkaConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentTriggerDisabledKeepsLegacyMessagesWithoutOutboxResources(t *testing.T) {
	for _, cfg := range []config.KafkaConfig{{}, {Brokers: []string{""}, TopicChat: "", TopicAgentTrigger: " \t"},
		{TopicChat: "same", TopicAgentTrigger: "same"}} {
		legacy := &runtimeMessages{}
		legacyCalls := 0
		runtime, err := newPushMessaging(cfg, nil, zap.NewNop(), agentTriggerFactories{
			legacy: func() repository.MessageRepository { legacyCalls++; return legacy },
			enabled: func(*gorm.DB, []string, string, *zap.Logger) pushMessaging {
				t.Fatal("disabled setup constructed Outbox resources")
				return pushMessaging{}
			},
		})
		if err != nil || runtime.messages != legacy || legacyCalls != 1 || runtime.publisher != nil || runtime.writer != nil {
			t.Fatalf("disabled runtime: %+v, %v", runtime, err)
		}
		if err := runtime.messages.Create(context.Background(), &model.Message{}); err != nil || legacy.creates.Load() != 1 {
			t.Fatal("disabled message did not use the original repository")
		}
	}
	// The production default can be assembled without a database connection while disabled.
	runtime, err := newPushMessaging(config.KafkaConfig{}, nil, zap.NewNop(), agentTriggerFactories{})
	if err != nil || runtime.messages == nil || runtime.publisher != nil || runtime.writer != nil {
		t.Fatalf("default disabled runtime: %+v %v", runtime, err)
	}
}

func TestAgentTriggerEnabledSelectsTransactionalMessagesPublisherAndDedicatedWriter(t *testing.T) {
	cfg := validTriggerKafkaConfig()
	cfg.Brokers = []string{" kafka:19092 ", "second:9092"}
	db, logger := &gorm.DB{}, zap.NewNop()
	transactional := &runtimeMessages{}
	publisher := pushRunnerFunc(func(context.Context) {})
	writer := pushCloseFunc(func() error { return nil })
	enabledCalls := 0
	runtime, err := newPushMessaging(cfg, db, logger, agentTriggerFactories{
		legacy: func() repository.MessageRepository { t.Fatal("enabled setup selected old repository"); return nil },
		enabled: func(actualDB *gorm.DB, brokers []string, topic string, actualLogger *zap.Logger) pushMessaging {
			enabledCalls++
			if actualDB != db || actualLogger != logger || topic != "agent_task_triggers" || !reflect.DeepEqual(brokers, []string{"kafka:19092", "second:9092"}) {
				t.Fatalf("wrong resources: %v %s", brokers, topic)
			}
			brokers[0] = "factory-owned-copy"
			return pushMessaging{messages: transactional, publisher: publisher, writer: writer}
		},
	})
	if err != nil || enabledCalls != 1 || runtime.messages != transactional || runtime.publisher == nil || runtime.writer == nil || cfg.Brokers[0] != " kafka:19092 " {
		t.Fatalf("enabled runtime: %+v %v", runtime, err)
	}
	if err := runtime.messages.Create(context.Background(), &model.Message{}); err != nil || transactional.creates.Load() != 1 {
		t.Fatal("enabled message did not reach the selected transactional repository")
	}
}

func TestProductionEnabledMessagingUsesOutboxRepositoryAndCancelSafePublisher(t *testing.T) {
	// Constructors do not query SQL or connect Kafka. Starting with an already
	// cancelled context also proves this real publisher exits before accessing either.
	runtime, err := newPushMessaging(validTriggerKafkaConfig(), &gorm.DB{}, zap.NewNop(), agentTriggerFactories{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.messages == nil || reflect.TypeOf(runtime.messages) == reflect.TypeOf(repository.NewMessageRepository()) {
		t.Fatal("enabled production setup retained the old message repository")
	}
	if _, ok := runtime.publisher.(*push.AgentTriggerPublisher); !ok {
		t.Fatal("enabled production setup omitted the real Outbox publisher")
	}
	writer, ok := runtime.writer.(*kafka.Writer)
	if !ok || writer.Topic != "agent_task_triggers" || writer.Async || writer.RequiredAcks != kafka.RequireAll {
		t.Fatalf("production writer is not synchronous and dedicated: %T", runtime.writer)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var consumerStopped atomic.Bool
	consumer := runtimeConsumer{start: func(actual context.Context) {
		if actual.Err() == nil {
			t.Error("production cancellation test started an active consumer")
		}
		consumerStopped.Store(true)
	}, close: func() error { return nil }}
	if err := startPushWorkers(ctx, consumer, runtime.publisher, runtime.writer)(); err != nil || !consumerStopped.Load() {
		t.Fatalf("cancelled production runtime did not stop and close: %v", err)
	}
}

func TestPushWorkersWaitForConsumerAndPublisherAfterCancelBeforeClosingResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerStarted, publisherStarted := make(chan struct{}), make(chan struct{})
	consumerCancelled, publisherCancelled := make(chan struct{}), make(chan struct{})
	consumerRelease, publisherRelease := make(chan struct{}), make(chan struct{})
	consumerExited, publisherExited := make(chan struct{}), make(chan struct{})
	closed := make(chan string, 2)
	start := func(started, cancelled, release, exited chan struct{}) func(context.Context) {
		return func(actual context.Context) {
			if actual != ctx {
				t.Error("workers do not share the process context")
			}
			close(started)
			<-actual.Done()
			close(cancelled)
			<-release
			close(exited)
		}
	}
	assertStopped := func() {
		select {
		case <-consumerExited:
		default:
			t.Error("consumer resource closed before its loop stopped")
		}
		select {
		case <-publisherExited:
		default:
			t.Error("writer closed before publisher stopped")
		}
	}
	consumer := runtimeConsumer{start: start(consumerStarted, consumerCancelled, consumerRelease, consumerExited),
		close: func() error { assertStopped(); closed <- "consumer"; return nil }}
	publisher := pushRunnerFunc(start(publisherStarted, publisherCancelled, publisherRelease, publisherExited))
	writer := pushCloseFunc(func() error { assertStopped(); closed <- "writer"; return nil })
	waitAndClose := startPushWorkers(ctx, consumer, publisher, writer)
	awaitRuntimeSignal(t, consumerStarted)
	awaitRuntimeSignal(t, publisherStarted)
	done := make(chan error, 1)
	go func() { done <- waitAndClose() }()
	select {
	case name := <-closed:
		t.Fatalf("closed before cancel: %s", name)
	default:
	}
	cancel()
	awaitRuntimeSignal(t, consumerCancelled)
	awaitRuntimeSignal(t, publisherCancelled)
	close(publisherRelease)
	awaitRuntimeSignal(t, publisherExited)
	select {
	case name := <-closed:
		t.Fatalf("closed while consumer still working: %s", name)
	default:
	}
	close(consumerRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not wait and close")
	}
	if first, second := <-closed, <-closed; first != "consumer" || second != "writer" {
		t.Fatalf("close order: %s then %s", first, second)
	}
}

func TestPushWorkersDisabledWaitForOldConsumerAndPropagateCloseFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, exited := make(chan struct{}), make(chan struct{})
	closeErr := errors.New("consumer close failed")
	var closes atomic.Int32
	consumer := runtimeConsumer{start: func(ctx context.Context) { close(started); <-ctx.Done(); close(exited) },
		close: func() error {
			select {
			case <-exited:
			default:
				t.Error("old consumer closed while still running")
			}
			closes.Add(1)
			return closeErr
		}}
	waitAndClose := startPushWorkers(ctx, consumer, nil, nil)
	awaitRuntimeSignal(t, started)
	cancel()
	if err := waitAndClose(); !errors.Is(err, closeErr) || closes.Load() != 1 {
		t.Fatalf("close error %v, count %d", err, closes.Load())
	}
}

func TestPushWorkersCloseWriterEvenWhenConsumerCloseFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	consumerErr, writerErr := errors.New("consumer failed"), errors.New("writer failed")
	var consumerStopped, publisherStopped, writerClosed atomic.Bool
	consumer := runtimeConsumer{start: func(context.Context) { consumerStopped.Store(true) }, close: func() error { return consumerErr }}
	publisher := pushRunnerFunc(func(context.Context) { publisherStopped.Store(true) })
	writer := pushCloseFunc(func() error {
		if !consumerStopped.Load() || !publisherStopped.Load() {
			t.Error("writer closed before loops ended")
		}
		writerClosed.Store(true)
		return writerErr
	})
	err := startPushWorkers(ctx, consumer, publisher, writer)()
	if !writerClosed.Load() || !errors.Is(err, consumerErr) || !errors.Is(err, writerErr) {
		t.Fatalf("resource close errors not preserved: %v", err)
	}
}
