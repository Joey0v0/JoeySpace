package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/push"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type notificationRuntimeReaderStub struct {
	fetch  func(context.Context) (kafka.Message, error)
	commit func(context.Context, ...kafka.Message) error
	close  func() error
}

func (r *notificationRuntimeReaderStub) FetchMessage(ctx context.Context) (kafka.Message, error) {
	return r.fetch(ctx)
}
func (r *notificationRuntimeReaderStub) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	if r.commit != nil {
		return r.commit(ctx, messages...)
	}
	return nil
}
func (r *notificationRuntimeReaderStub) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

type notificationRuntimeClientStub struct {
	send  func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error)
	close func() error
}

func (c *notificationRuntimeClientStub) Send(ctx context.Context, addr string, event model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
	if c.send != nil {
		return c.send(ctx, addr, event)
	}
	return model.TaskNotificationDelivery{NotificationID: event.NotificationID, Outcome: model.TaskNotificationQueued}, nil
}
func (c *notificationRuntimeClientStub) Close() error {
	if c.close != nil {
		return c.close()
	}
	return nil
}

type notificationRuntimeOnlineStub func(context.Context, int64) (string, error)

func (f notificationRuntimeOnlineStub) GetOnline(ctx context.Context, id int64) (string, error) {
	return f(ctx, id)
}

func notificationRuntimeConfig() *config.Config {
	return &config.Config{Kafka: config.KafkaConfig{Brokers: []string{"kafka:19092"}, TopicChat: "chat_messages", TopicAgentTrigger: "agent_task_triggers", ConsumerGroup: "im_push_group"},
		TaskNotifications: config.TaskNotificationsConfig{Push: config.TaskNotificationPushConfig{Enabled: true, Topic: "task_notices", ConsumerGroup: "task_notice_push", WSDNSName: "ws.go-im.internal",
			Routes: map[string]string{"im-ws:9091": "https://im-ws:9443"}, TLS: config.NotificationTLSConfig{CertFile: "private-push.pem", KeyFile: "private-push.key", CAFile: "private-ca.pem"}}}}
}

func blockedNotificationReader() *notificationRuntimeReaderStub {
	return &notificationRuntimeReaderStub{fetch: func(ctx context.Context) (kafka.Message, error) {
		<-ctx.Done()
		return kafka.Message{}, ctx.Err()
	}}
}

func notificationRuntimeFactories(reader push.TaskNotificationReader, client notificationRuntimeSender) taskNotificationPushFactories {
	return taskNotificationPushFactories{client: func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error) { return client, nil },
		reader: func(kafka.ReaderConfig) push.TaskNotificationReader { return reader }}
}

func TestTaskNotificationPushDisabledUsesNoFactoriesOrCredentials(t *testing.T) {
	cfg := &config.Config{TaskNotifications: config.TaskNotificationsConfig{WS: config.TaskNotificationWSConfig{Enabled: true}}}
	factories := taskNotificationPushFactories{
		client: func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error) {
			t.Fatal("disabled TLS factory used")
			return nil, nil
		},
		reader: func(kafka.ReaderConfig) push.TaskNotificationReader {
			t.Fatal("disabled reader factory used")
			return nil
		},
	}
	runtime, err := prepareTaskNotificationPush(cfg, nil, factories)
	if runtime != nil || err != nil {
		t.Fatalf("disabled runtime=%v error=%v", runtime, err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("disabled cleanup=%v", err)
	}
}

func TestTaskNotificationPushInvalidEnabledConfigFailsBeforeFactories(t *testing.T) {
	for _, mutate := range []func(*config.Config){
		func(c *config.Config) { c.Kafka.Brokers = nil },
		func(c *config.Config) { c.TaskNotifications.Push.Topic = c.Kafka.TopicChat },
		func(c *config.Config) { c.TaskNotifications.Push.ConsumerGroup = c.Kafka.ConsumerGroup },
		func(c *config.Config) { c.TaskNotifications.Push.TLS.CAFile = "" },
		func(c *config.Config) { c.TaskNotifications.Push.Routes["im-ws:9091"] = "http://im-ws:9443" },
	} {
		cfg := notificationRuntimeConfig()
		mutate(cfg)
		calls := 0
		factories := taskNotificationPushFactories{client: func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error) { calls++; return nil, nil },
			reader: func(kafka.ReaderConfig) push.TaskNotificationReader { calls++; return nil }}
		if runtime, err := prepareTaskNotificationPush(cfg, nil, factories); runtime != nil || err == nil || calls != 0 {
			t.Fatalf("invalid config runtime=%v error=%v factories=%d", runtime, err, calls)
		}
	}
	if runtime, err := prepareTaskNotificationPush(nil, nil, taskNotificationPushFactories{}); runtime != nil || err == nil {
		t.Fatalf("nil config runtime=%v error=%v", runtime, err)
	}
}

func TestTaskNotificationPushTLSPrecedesIndependentSynchronousReaderAndCopiesConfiguration(t *testing.T) {
	cfg := notificationRuntimeConfig()
	steps := []string{}
	var readerCfg kafka.ReaderConfig
	reader, client := blockedNotificationReader(), &notificationRuntimeClientStub{}
	factories := taskNotificationPushFactories{
		client: func(c push.TaskNotificationClientConfig) (notificationRuntimeSender, error) {
			steps = append(steps, "TLS")
			if c.WSDNSName != cfg.TaskNotifications.Push.WSDNSName || c.Files.CertFile != cfg.TaskNotifications.Push.TLS.CertFile || c.Files.KeyFile != cfg.TaskNotifications.Push.TLS.KeyFile || c.Files.CAFile != cfg.TaskNotifications.Push.TLS.CAFile || c.Routes["im-ws:9091"] != "https://im-ws:9443" {
				t.Errorf("TLS client configuration=%v", c)
			}
			c.Routes["im-ws:9091"] = "https://changed:9443"
			return client, nil
		},
		reader: func(c kafka.ReaderConfig) push.TaskNotificationReader {
			steps = append(steps, "reader")
			readerCfg = c
			return reader
		},
	}
	runtime, err := prepareTaskNotificationPush(cfg, nil, factories)
	if err != nil || runtime == nil {
		t.Fatalf("prepare=%v error=%v", runtime, err)
	}
	defer runtime.Close()
	if !reflect.DeepEqual(steps, []string{"TLS", "reader"}) || cfg.TaskNotifications.Push.Routes["im-ws:9091"] != "https://im-ws:9443" {
		t.Fatalf("init order=%v original routes=%v", steps, cfg.TaskNotifications.Push.Routes)
	}
	if readerCfg.Topic != "task_notices" || readerCfg.GroupID != "task_notice_push" || readerCfg.CommitInterval != 0 || readerCfg.StartOffset != kafka.FirstOffset || readerCfg.MinBytes != 1 || readerCfg.MaxBytes != 1e6 || readerCfg.QueueCapacity != 1 {
		t.Fatalf("unsafe reader configuration=%+v", readerCfg)
	}
	readerCfg.Brokers[0] = "changed:19092"
	if cfg.Kafka.Brokers[0] != "kafka:19092" {
		t.Fatal("reader factory changed shared Kafka brokers")
	}
}

func TestTaskNotificationPushClientFailureCleansPartialClientWithoutReader(t *testing.T) {
	for _, partial := range []bool{false, true} {
		closes, reads := 0, 0
		client := &notificationRuntimeClientStub{close: func() error { closes++; return errors.New("private cleanup detail") }}
		factories := taskNotificationPushFactories{
			client: func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error) {
				if partial {
					return client, errors.New("private credential path")
				}
				return nil, errors.New("private credential path")
			},
			reader: func(kafka.ReaderConfig) push.TaskNotificationReader { reads++; return nil },
		}
		runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, factories)
		wantCloses := 0
		if partial {
			wantCloses = 1
		}
		if runtime != nil || err == nil || strings.Contains(err.Error(), "private") || reads != 0 || closes != wantCloses {
			t.Fatalf("client init failure runtime=%v error=%v reader=%d closes=%d", runtime, err, reads, closes)
		}
	}
}

func TestTaskNotificationPushUnreadableProductionCertificatesDoNotConstructReader(t *testing.T) {
	reads := 0
	cfg := notificationRuntimeConfig()
	missing := t.TempDir()
	cfg.TaskNotifications.Push.TLS = config.NotificationTLSConfig{CertFile: filepath.Join(missing, "private-push.pem"), KeyFile: filepath.Join(missing, "private-push.key"), CAFile: filepath.Join(missing, "private-ca.pem")}
	runtime, err := prepareTaskNotificationPush(cfg, nil, taskNotificationPushFactories{
		reader: func(kafka.ReaderConfig) push.TaskNotificationReader { reads++; return nil },
	})
	if runtime != nil || err == nil || reads != 0 || strings.Contains(err.Error(), "private-push") {
		t.Fatalf("real credential failure runtime=%v error=%v reader=%d", runtime, err, reads)
	}
}

func TestTaskNotificationPushNilAndTypedNilFactoriesFailSafelyAndCleanClient(t *testing.T) {
	for _, typed := range []bool{false, true} {
		closes := 0
		client := &notificationRuntimeClientStub{close: func() error { closes++; return nil }}
		factories := taskNotificationPushFactories{
			client: func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error) { return client, nil },
			reader: func(kafka.ReaderConfig) push.TaskNotificationReader {
				if typed {
					var missing *notificationRuntimeReaderStub
					return missing
				}
				return nil
			},
		}
		if runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, factories); runtime != nil || err == nil || closes != 1 {
			t.Fatalf("nil reader runtime=%v error=%v cleanup=%d", runtime, err, closes)
		}
	}
	var missing *notificationRuntimeClientStub
	reads := 0
	factories := taskNotificationPushFactories{
		client: func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error) { return missing, nil },
		reader: func(kafka.ReaderConfig) push.TaskNotificationReader { reads++; return nil },
	}
	if runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, factories); runtime != nil || err == nil || reads != 0 {
		t.Fatalf("typed nil client runtime=%v error=%v reader=%d", runtime, err, reads)
	}
}

func TestTaskNotificationPushUnstartedCloseIsIdempotentAndClosesAllResourcesOnError(t *testing.T) {
	steps := []string{}
	reader := blockedNotificationReader()
	reader.close = func() error { steps = append(steps, "reader"); return errors.New("private broker close detail") }
	client := &notificationRuntimeClientStub{close: func() error { steps = append(steps, "client"); return errors.New("private certificate close detail") }}
	runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, notificationRuntimeFactories(reader, client))
	if err != nil {
		t.Fatal(err)
	}
	first, second := runtime.Close(), runtime.Close()
	if first == nil || second == nil || first.Error() != second.Error() || strings.Contains(first.Error(), "private") || !reflect.DeepEqual(steps, []string{"reader", "client"}) {
		t.Fatalf("cleanup first=%v second=%v steps=%v", first, second, steps)
	}
	if err := runtime.Start(context.Background(), notificationRuntimeOnlineStub(func(context.Context, int64) (string, error) { return "", nil })); err == nil {
		t.Fatal("closed runtime started")
	}
}

func TestTaskNotificationPushStartRejectsInvalidInputsWithoutClaimingRunning(t *testing.T) {
	reader, client := blockedNotificationReader(), &notificationRuntimeClientStub{}
	runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, notificationRuntimeFactories(reader, client))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	online := notificationRuntimeOnlineStub(func(context.Context, int64) (string, error) { return "", nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.Start(nil, online); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := runtime.Start(ctx, online); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start=%v", err)
	}
	if err := runtime.Start(context.Background(), nil); err == nil {
		t.Fatal("nil online locator accepted")
	}
	var missing notificationRuntimeOnlineStub
	if err := runtime.Start(context.Background(), missing); err == nil {
		t.Fatal("typed nil online locator accepted")
	}
	if runtime.started {
		t.Fatal("failed Start claimed running")
	}
	if err := runtime.Start(context.Background(), online); err != nil {
		t.Fatalf("valid start after rejection=%v", err)
	}
	if err := runtime.Start(context.Background(), online); err == nil {
		t.Fatal("second Start accepted")
	}
}

func TestTaskNotificationPushCloseCancelsAndWaitsBeforeResourcesWithoutCancelingChat(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var exited atomic.Bool
	var stepsMu sync.Mutex
	steps := []string{}
	appendStep := func(step string) { stepsMu.Lock(); steps = append(steps, step); stepsMu.Unlock() }
	reader := &notificationRuntimeReaderStub{fetch: func(ctx context.Context) (kafka.Message, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		exited.Store(true)
		appendStep("worker finished")
		return kafka.Message{}, ctx.Err()
	}, close: func() error {
		if !exited.Load() {
			t.Error("reader closed during active I/O")
		}
		appendStep("reader closed")
		return nil
	}}
	client := &notificationRuntimeClientStub{close: func() error {
		if !exited.Load() {
			t.Error("TLS client closed during active I/O")
		}
		appendStep("client closed")
		return nil
	}}
	runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, notificationRuntimeFactories(reader, client))
	if err != nil {
		t.Fatal(err)
	}
	chatContext, cancelChat := context.WithCancel(context.Background())
	defer cancelChat()
	if err := runtime.Start(chatContext, notificationRuntimeOnlineStub(func(context.Context, int64) (string, error) { return "", nil })); err != nil {
		t.Fatal(err)
	}
	awaitRuntimeSignal(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	awaitRuntimeSignal(t, canceled)
	select {
	case err := <-closed:
		t.Fatalf("cleanup returned before worker finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification cleanup did not finish")
	}
	if chatContext.Err() != nil || !reflect.DeepEqual(steps, []string{"worker finished", "reader closed", "client closed"}) {
		t.Fatalf("chat context=%v shutdown steps=%v", chatContext.Err(), steps)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskNotificationPushBadEventStopsOnlyNotificationWorkerWithoutCommitOrAutomaticClose(t *testing.T) {
	var fetches, commits, closes atomic.Int32
	reader := &notificationRuntimeReaderStub{fetch: func(context.Context) (kafka.Message, error) {
		fetches.Add(1)
		return kafka.Message{Topic: "task_notices", Partition: 0, Offset: 7, Key: []byte("private-bad-key"), Value: []byte("private malformed event")}, nil
	}, commit: func(context.Context, ...kafka.Message) error { commits.Add(1); return nil }, close: func() error { closes.Add(1); return nil }}
	core, logs := observer.New(zap.ErrorLevel)
	runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), zap.New(core), notificationRuntimeFactories(reader, &notificationRuntimeClientStub{}))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runtime.Start(parent, notificationRuntimeOnlineStub(func(context.Context, int64) (string, error) {
		t.Error("bad event looked up online status")
		return "", nil
	})); err != nil {
		t.Fatal(err)
	}
	awaitRuntimeSignal(t, runtime.done)
	if fetches.Load() != 1 || commits.Load() != 0 || closes.Load() != 0 || parent.Err() != nil {
		t.Fatalf("bad event fetch/commit/close=%d/%d/%d parent=%v", fetches.Load(), commits.Load(), closes.Load(), parent.Err())
	}
	if err := runtime.Start(parent, notificationRuntimeOnlineStub(func(context.Context, int64) (string, error) { return "", nil })); err == nil {
		t.Fatal("halted runtime was silently restarted")
	}
	entries := logs.All()
	if len(entries) != 2 || entries[0].Message != "task notification event rejected" ||
		entries[1].Message != "task notification consumer stopped; invalid event remains uncommitted" ||
		len(entries[1].Context) != 0 || strings.Contains(entries[0].Message, "private") || strings.Contains(entries[1].Message, "private") {
		t.Fatalf("unsafe halted-consumer log=%v", entries)
	}
	fields := entries[0].ContextMap()
	if fields["phase"] != "event_validation" || fields["topic"] != "task_notices" ||
		fields["partition"] != int64(0) || fields["offset"] != int64(7) || len(fields) != 4 {
		t.Fatalf("bad event log must contain only safe location fields: %v", fields)
	}
}

func TestTaskNotificationPushProductionConsumerCommitsOfflineHintBeforeFetchingAgain(t *testing.T) {
	event := model.TaskNotificationEvent{Version: 1, Type: model.TaskNotificationEventType, NotificationID: 9, TeamID: 200, RecipientID: 42}
	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	committed, again := make(chan struct{}), make(chan struct{})
	var fetches atomic.Int32
	reader := &notificationRuntimeReaderStub{fetch: func(ctx context.Context) (kafka.Message, error) {
		if fetches.Add(1) == 1 {
			return kafka.Message{Topic: "task_notices", Partition: 0, Offset: 7, Key: []byte(event.Key()), Value: wire}, nil
		}
		select {
		case <-committed:
		default:
			t.Error("fetched past uncommitted notification")
		}
		close(again)
		<-ctx.Done()
		return kafka.Message{}, ctx.Err()
	}, commit: func(ctx context.Context, messages ...kafka.Message) error {
		if len(messages) != 1 || messages[0].Offset != 7 || ctx.Err() != nil {
			t.Errorf("wrong commit=%v context=%v", messages, ctx.Err())
		}
		close(committed)
		return nil
	}}
	client := &notificationRuntimeClientStub{send: func(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
		t.Error("offline hint attempted HTTPS send")
		return model.TaskNotificationDelivery{}, nil
	}}
	runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, notificationRuntimeFactories(reader, client))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.Start(context.Background(), notificationRuntimeOnlineStub(func(_ context.Context, id int64) (string, error) {
		if id != event.RecipientID {
			t.Errorf("wrong recipient=%d", id)
		}
		return "", nil
	})); err != nil {
		t.Fatal(err)
	}
	awaitRuntimeSignal(t, again)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskNotificationPushConcurrentCloseCallsReleaseEachResourceOnce(t *testing.T) {
	var readerCloses, clientCloses atomic.Int32
	reader := blockedNotificationReader()
	reader.close = func() error { readerCloses.Add(1); return nil }
	client := &notificationRuntimeClientStub{close: func() error { clientCloses.Add(1); return nil }}
	runtime, err := prepareTaskNotificationPush(notificationRuntimeConfig(), nil, notificationRuntimeFactories(reader, client))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background(), notificationRuntimeOnlineStub(func(context.Context, int64) (string, error) { return "", nil })); err != nil {
		t.Fatal(err)
	}
	var calls sync.WaitGroup
	for i := 0; i < 3; i++ {
		calls.Add(1)
		go func() {
			defer calls.Done()
			if err := runtime.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	calls.Wait()
	if readerCloses.Load() != 1 || clientCloses.Load() != 1 {
		t.Fatalf("duplicate resource closes=%d/%d", readerCloses.Load(), clientCloses.Load())
	}
}
