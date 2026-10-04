package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/segmentio/kafka-go"
	agent "github.com/yjydist/go-im/rpc/agent"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func inboxRuntimeEnvironment() map[string]string {
	return map[string]string{"AGENT_TRIGGER_INBOX_ENABLED": "true", "AGENT_TRIGGER_KAFKA_BROKERS": "kafka:19092,127.0.0.1:9092,[::1]:9092",
		"AGENT_TRIGGER_KAFKA_TOPIC": "agent_task_triggers", "AGENT_TRIGGER_KAFKA_GROUP": "agent_trigger_inbox"}
}

func inboxRuntimeConfig() agentTriggerInboxConfig {
	return agentTriggerInboxConfig{Enabled: true, Brokers: []string{"kafka:19092"}, Topic: "agent_task_triggers", Group: "agent_trigger_inbox"}
}

func TestAgentTriggerInboxDisabledDoesNotReadOrConstructKafka(t *testing.T) {
	for _, value := range []string{"", "false"} {
		cfg, err := loadAgentTriggerInboxConfig(func(key string) string {
			if key != "AGENT_TRIGGER_INBOX_ENABLED" {
				t.Fatalf("disabled configuration read %s", key)
			}
			return value
		})
		if err != nil || cfg.Enabled {
			t.Fatalf("%v %v", cfg, err)
		}
		inbox, err := newAgentTriggerInbox(cfg, nil, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) {
			t.Fatal("disabled factory reached")
			return nil, nil
		})
		if err != nil || inbox != nil || inbox.Start(context.Background(), nil) != nil || inbox.Stop() != nil || inbox.stoppedError() != nil {
			t.Fatalf("disabled runtime: %v %v", inbox, err)
		}
	}
	if _, err := loadAgentTriggerInboxConfig(nil); err == nil {
		t.Fatal("nil environment reader accepted")
	}
}

func TestAgentTriggerInboxRejectsInvalidConfigurationBeforeResources(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AGENT_TRIGGER_INBOX_ENABLED", "TRUE"}, {"AGENT_TRIGGER_INBOX_ENABLED", "1"}, {"AGENT_TRIGGER_INBOX_ENABLED", " true"},
		{"AGENT_TRIGGER_INBOX_ENABLED", "false "}, {"AGENT_TRIGGER_INBOX_ENABLED", "\t"},
		{"AGENT_TRIGGER_KAFKA_BROKERS", ""}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka"}, {"AGENT_TRIGGER_KAFKA_BROKERS", ":9092"},
		{"AGENT_TRIGGER_KAFKA_BROKERS", "kafka:0"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka:65536"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka:+9092"},
		{"AGENT_TRIGGER_KAFKA_BROKERS", "kafka:09092"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka:9092,"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka:9092, redis:9092"},
		{"AGENT_TRIGGER_KAFKA_BROKERS", "http://kafka:9092"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "dns:///kafka:9092"},
		{"AGENT_TRIGGER_KAFKA_BROKERS", "[kafka]:9092"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka\u2003name:9092"},
		{"AGENT_TRIGGER_KAFKA_TOPIC", ""}, {"AGENT_TRIGGER_KAFKA_TOPIC", "chat_messages"}, {"AGENT_TRIGGER_KAFKA_TOPIC", "."},
		{"AGENT_TRIGGER_KAFKA_TOPIC", ".."}, {"AGENT_TRIGGER_KAFKA_TOPIC", "a/b"}, {"AGENT_TRIGGER_KAFKA_TOPIC", "通知"},
		{"AGENT_TRIGGER_KAFKA_TOPIC", " triggers"}, {"AGENT_TRIGGER_KAFKA_TOPIC", "triggers\u2003"}, {"AGENT_TRIGGER_KAFKA_TOPIC", strings.Repeat("a", 250)},
		{"AGENT_TRIGGER_KAFKA_GROUP", ""}, {"AGENT_TRIGGER_KAFKA_GROUP", "im_push_group"}, {"AGENT_TRIGGER_KAFKA_GROUP", "group name"},
		{"AGENT_TRIGGER_KAFKA_GROUP", "group\u2003name"}, {"AGENT_TRIGGER_KAFKA_GROUP", "group\x00name"},
		{"AGENT_TRIGGER_KAFKA_GROUP", "group\xffname"}, {"AGENT_TRIGGER_KAFKA_BROKERS", "kafka\xff:9092"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			values := inboxRuntimeEnvironment()
			values[tc.key] = tc.value
			if _, err := loadAgentTriggerInboxConfig(func(key string) string { return values[key] }); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	for _, cfg := range []agentTriggerInboxConfig{{Enabled: true}, {Enabled: true, Brokers: []string{"kafka:9092"}, Topic: "chat_messages", Group: "private-group"}} {
		if runtime, err := newAgentTriggerInbox(cfg, nil, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) {
			t.Fatal("invalid configuration constructed Kafka")
			return nil, nil
		}); err == nil || runtime != nil {
			t.Fatal("invalid runtime constructed")
		}
	}
	// An invalid switch/topic wins over even an invalid DSN: no DB or model is prepared.
	t.Setenv("AGENT_MYSQL_DSN", "private-invalid-dsn")
	t.Setenv("AGENT_TRIGGER_INBOX_ENABLED", "invalid")
	if err := runAgent(zrpc.RpcServerConf{}); err == nil || !strings.Contains(err.Error(), "switch") {
		t.Fatalf("configuration did not precede DB: %v", err)
	}
	t.Setenv("AGENT_TRIGGER_INBOX_ENABLED", "true")
	t.Setenv("AGENT_TRIGGER_KAFKA_BROKERS", "kafka:9092")
	t.Setenv("AGENT_TRIGGER_KAFKA_TOPIC", "chat_messages")
	t.Setenv("AGENT_TRIGGER_KAFKA_GROUP", "private-group")
	if err := runAgent(zrpc.RpcServerConf{}); err == nil || !strings.Contains(err.Error(), "topic") || strings.Contains(err.Error(), "private") {
		t.Fatalf("topic did not precede DB: %v", err)
	}
}

type inboxRuntimeTestReader struct {
	fetch       func(context.Context) (kafka.Message, error)
	commit      func(context.Context, ...kafka.Message) error
	close       func() error
	fetchCalls  atomic.Int32
	commitCalls atomic.Int32
	closeCalls  atomic.Int32
}

func (r *inboxRuntimeTestReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	r.fetchCalls.Add(1)
	if r.fetch != nil {
		return r.fetch(ctx)
	}
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (r *inboxRuntimeTestReader) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	r.commitCalls.Add(1)
	if r.commit != nil {
		return r.commit(ctx, messages...)
	}
	return nil
}

func (r *inboxRuntimeTestReader) Close() error {
	r.closeCalls.Add(1)
	if r.close != nil {
		return r.close()
	}
	return nil
}

func TestAgentTriggerInboxBuildsOnlyDedicatedSynchronousReader(t *testing.T) {
	values := inboxRuntimeEnvironment()
	cfg, err := loadAgentTriggerInboxConfig(func(key string) string { return values[key] })
	if err != nil || len(cfg.Brokers) != 3 || cfg.Brokers[2] != "[::1]:9092" {
		t.Fatalf("valid config: %v %v", cfg, err)
	}
	reader := &inboxRuntimeTestReader{}
	inbox, err := newAgentTriggerInbox(cfg, &gorm.DB{}, func(config kafka.ReaderConfig) (agent.TriggerEventReader, error) {
		if config.GroupID != cfg.Group || config.Topic != cfg.Topic || config.CommitInterval != 0 || config.StartOffset != kafka.FirstOffset ||
			config.MinBytes != 1 || config.MaxBytes != 64*1024 || len(config.Brokers) != 3 || config.GroupID == "im_push_group" || config.Topic == "chat_messages" {
			t.Fatalf("Reader config: %+v", config)
		}
		config.Brokers[0] = "mutated:9092"
		return reader, nil
	})
	if err != nil || inbox == nil || cfg.Brokers[0] != "kafka:19092" || reader.fetchCalls.Load() != 0 {
		t.Fatalf("constructed/started unexpectedly: %v %v", inbox, err)
	}
	if inbox.Stop() != nil || inbox.Stop() != nil || reader.closeCalls.Load() != 1 || inbox.Start(context.Background(), nil) == nil {
		t.Fatal("prepared but unstarted reader was not closed once")
	}
	if _, err := newAgentTriggerInbox(inboxRuntimeConfig(), nil, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) {
		t.Fatal("missing DB reached factory")
		return nil, nil
	}); err == nil {
		t.Fatal("missing DB accepted")
	}
}

func TestAgentTriggerInboxFactoryFailureClosesPreparedResourceAndMasksDetails(t *testing.T) {
	for _, withResource := range []bool{false, true} {
		reader := &inboxRuntimeTestReader{}
		inbox, err := newAgentTriggerInbox(inboxRuntimeConfig(), &gorm.DB{}, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) {
			if withResource {
				return reader, errors.New("private-broker-address and credentials")
			}
			return nil, errors.New("private-broker-address and credentials")
		})
		wantCloses := int32(0)
		if withResource {
			wantCloses = 1
		}
		if inbox != nil || err == nil || strings.Contains(err.Error(), "private") || reader.closeCalls.Load() != wantCloses {
			t.Fatalf("resource cleanup %v: %v %v", withResource, inbox, err)
		}
	}
	if inbox, err := newAgentTriggerInbox(inboxRuntimeConfig(), &gorm.DB{}, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) { return nil, nil }); inbox != nil || err == nil {
		t.Fatal("nil reader accepted")
	}
	var absentReader *inboxRuntimeTestReader
	if inbox, err := newAgentTriggerInbox(inboxRuntimeConfig(), &gorm.DB{}, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) { return absentReader, nil }); inbox != nil || err == nil {
		t.Fatal("typed nil reader accepted")
	}
}

func TestAgentTriggerInboxProductionConsumerPersistsQueuedBeforeCommit(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_task_trigger_inbox (message_id, action, event_version, status) VALUES (?, ?, ?, ?)")).
		WithArgs(int64(9007199254740993), "extract_tasks", 1, "queued").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	acknowledged := make(chan struct{})
	var fetched atomic.Int32
	reader := &inboxRuntimeTestReader{
		fetch: func(ctx context.Context) (kafka.Message, error) {
			if fetched.Add(1) == 1 {
				return kafka.Message{Topic: "agent_task_triggers", Partition: 1, Offset: 20, Key: []byte("agent-trigger:tasks:9007199254740993"),
					Value: []byte(`{"version":1,"action":"extract_tasks","message_id":"9007199254740993"}`), Headers: []kafka.Header{{Key: "authorization", Value: []byte("not-a-token")}}}, nil
			}
			<-ctx.Done()
			return kafka.Message{}, ctx.Err()
		},
		commit: func(_ context.Context, messages ...kafka.Message) error {
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("Kafka committed before durable SQL: %v", err)
			}
			if len(messages) != 1 || messages[0].Offset != 20 {
				t.Errorf("wrong commit: %v", messages)
			}
			close(acknowledged)
			return nil
		},
	}
	inbox, err := newAgentTriggerInbox(inboxRuntimeConfig(), db, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) { return reader, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Stop()
	if err := inbox.Start(context.Background(), func(err error) { t.Errorf("unexpected consumer stop: %v", err) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-acknowledged:
	case <-time.After(2 * time.Second):
		t.Fatal("notification was not persisted/committed")
	}
	if err := inbox.Stop(); err != nil || reader.commitCalls.Load() != 1 || reader.closeCalls.Load() != 1 || inbox.stoppedError() != nil {
		t.Fatalf("%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type inboxRuntimeTestConsumer struct {
	run   func(context.Context) error
	close func() error
}

func (c *inboxRuntimeTestConsumer) Run(ctx context.Context) error { return c.run(ctx) }
func (c *inboxRuntimeTestConsumer) Close() error                  { return c.close() }

func TestAgentTriggerInboxStopCancelsWaitsThenClosesExactlyOnce(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	inbox := &agentTriggerInbox{consumer: &inboxRuntimeTestConsumer{
		run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return ctx.Err()
		},
		close: func() error { closes.Add(1); return errors.New("private-close-detail") },
	}}
	if err := inbox.Start(context.Background(), func(err error) { t.Errorf("normal cancellation reported failure: %v", err) }); err != nil {
		t.Fatal(err)
	}
	<-started
	if inbox.Start(context.Background(), nil) == nil {
		t.Fatal("second start accepted")
	}
	stopped := make(chan error, 2)
	go func() { stopped <- inbox.Stop() }()
	<-cancelled
	if closes.Load() != 0 {
		t.Fatal("reader closed before Run exited")
	}
	select {
	case <-stopped:
		t.Fatal("stop returned before Run exited")
	default:
	}
	go func() { stopped <- inbox.Stop() }()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-stopped; err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe close error: %v", err)
		}
	}
	if closes.Load() != 1 || inbox.stoppedError() != nil {
		t.Fatalf("closes=%d failure=%v", closes.Load(), inbox.stoppedError())
	}
}

func TestAgentTriggerInboxParentCancellationIsNotAnAbnormalStop(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
		}
		var reports atomic.Int32
		inbox := &agentTriggerInbox{consumer: &inboxRuntimeTestConsumer{run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, close: func() error { return nil }}}
		if err := inbox.Start(ctx, func(error) { reports.Add(1) }); err != nil {
			t.Fatal(err)
		}
		if !deadline {
			cancel()
		}
		select {
		case <-inbox.done:
		case <-time.After(time.Second):
			t.Fatal("parent cancellation ignored")
		}
		cancel()
		if inbox.Stop() != nil || reports.Load() != 0 || inbox.stoppedError() != nil {
			t.Fatal("normal stop treated as failure")
		}
	}
	if (&agentTriggerInbox{}).Start(context.Background(), nil) == nil {
		t.Fatal("missing consumer accepted")
	}
	inbox := &agentTriggerInbox{consumer: &inboxRuntimeTestConsumer{run: func(context.Context) error { return nil }, close: func() error { return nil }}}
	if inbox.Start(nil, nil) == nil {
		t.Fatal("nil parent accepted")
	}
	_ = inbox.Stop()
}

func TestAgentTriggerInboxAbnormalStopIsSafeRecordedOnceAndNeverRestarted(t *testing.T) {
	for _, failure := range []error{fmt.Errorf("%w: private SQL row", agent.ErrInvalidTriggerInbox), errors.New("private broker credentials"), nil} {
		var runs, reports atomic.Int32
		inbox := &agentTriggerInbox{consumer: &inboxRuntimeTestConsumer{
			run: func(context.Context) error { runs.Add(1); return failure }, close: func() error { return nil },
		}}
		if err := inbox.Start(context.Background(), func(err error) {
			reports.Add(1)
			if strings.Contains(err.Error(), "private") {
				t.Error("private error leaked")
			}
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-inbox.done:
		case <-time.After(time.Second):
			t.Fatal("consumer did not stop")
		}
		if inbox.stoppedError() == nil || strings.Contains(inbox.stoppedError().Error(), "private") || runs.Load() != 1 || reports.Load() != 1 || inbox.Start(context.Background(), nil) == nil {
			t.Fatal("abnormal consumer not stopped once")
		}
		_ = inbox.Stop()
	}
}

type inboxRuntimeAnswerer struct {
	t     *testing.T
	calls atomic.Int32
}

func (a *inboxRuntimeAnswerer) Answer(_ context.Context, token string, team, group int64, question string) (string, error) {
	if token != "person-token" || team != 200 || group != 300 || question != "本人问答" {
		a.t.Error("ordinary person RPC behavior changed")
	}
	a.calls.Add(1)
	return "本人问答仍可用", nil
}

func TestAgentTriggerInboxInvalidNotificationDoesNotAcknowledgeOrStopPersonRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	answerer := &inboxRuntimeAnswerer{t: t}
	agentpb.RegisterAgentServer(server, agent.NewServer(answerer))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := agentpb.NewAgentClient(conn)
	ask := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer person-token"))
		result, err := client.Ask(ctx, &agentpb.AskRequest{TeamId: 200, GroupId: 300, Question: "本人问答"})
		if err != nil || result.GetAnswer() != "本人问答仍可用" {
			t.Fatalf("ordinary RPC stopped: %v %v", result, err)
		}
	}
	ask()
	reader := &inboxRuntimeTestReader{fetch: func(context.Context) (kafka.Message, error) {
		return kafka.Message{Key: []byte("private-key"), Value: []byte(`{"version":1,"action":"extract_tasks","message_id":"1","private":"credentials"}`)}, nil
	}}
	inbox, err := newAgentTriggerInbox(inboxRuntimeConfig(), &gorm.DB{}, func(kafka.ReaderConfig) (agent.TriggerEventReader, error) { return reader, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Stop()
	var reports atomic.Int32
	if err := inbox.Start(context.Background(), func(err error) {
		reports.Add(1)
		if strings.Contains(err.Error(), "private") {
			t.Error("notification leaked")
		}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inbox.done:
	case <-time.After(time.Second):
		t.Fatal("invalid notification did not stop consumer")
	}
	ask()
	if reader.fetchCalls.Load() != 1 || reader.commitCalls.Load() != 0 || reader.closeCalls.Load() != 0 || reports.Load() != 1 ||
		inbox.stoppedError() == nil || !strings.Contains(inbox.stoppedError().Error(), "without acknowledgement") || answerer.calls.Load() != 2 {
		t.Fatal("A62 isolation or acknowledgement violated")
	}
	_ = inbox.Stop()
	if reader.closeCalls.Load() != 1 {
		t.Fatal("reader not closed on final process cleanup")
	}
}

// Compile-time contracts keep the factory/lifecycle tests on the production interfaces.
var _ agent.TriggerEventReader = (*inboxRuntimeTestReader)(nil)
var _ agentTriggerInboxConsumer = (*inboxRuntimeTestConsumer)(nil)
var _ agent.Answerer = (*inboxRuntimeAnswerer)(nil)
