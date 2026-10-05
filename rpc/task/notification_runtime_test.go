package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

func notificationEnvironment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestTaskNotificationPublishConfigDisabledDoesNotRequireInfrastructure(t *testing.T) {
	for _, flag := range []string{"", "false"} {
		cfg, err := loadTaskNotificationPublishConfig(notificationEnvironment(map[string]string{
			"TASK_NOTIFICATION_PUBLISH_ENABLED": flag, "TASK_NOTIFICATION_KAFKA_BROKERS": "bad://secret", "TASK_NOTIFICATION_TOPIC": "bad topic",
		}))
		if err != nil || cfg.Enabled {
			t.Fatalf("disabled config: %#v %v", cfg, err)
		}
		runtime, err := newTaskNotificationRuntime(nil, cfg, nil, func(*gorm.DB, taskNotificationPublishConfig, *zap.Logger) taskNotificationRuntime {
			t.Fatal("disabled publisher created infrastructure")
			return taskNotificationRuntime{}
		})
		if err != nil || runtime.runner != nil || runtime.writer != nil {
			t.Fatalf("disabled runtime: %#v %v", runtime, err)
		}
		stop := startTaskNotificationPublisher(context.Background(), runtime)
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskNotificationPublishConfigRejectsUnsafeEnabledSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"TASK_NOTIFICATION_PUBLISH_ENABLED", "1"}, {"TASK_NOTIFICATION_PUBLISH_ENABLED", "FALSE"},
		{"TASK_NOTIFICATION_TOPIC", ""}, {"TASK_NOTIFICATION_TOPIC", "chat_messages"}, {"TASK_NOTIFICATION_TOPIC", "agent_task_triggers"},
		{"TASK_NOTIFICATION_TOPIC", ".."}, {"TASK_NOTIFICATION_TOPIC", "bad topic"}, {"TASK_NOTIFICATION_TOPIC", strings.Repeat("a", 250)},
		{"TASK_NOTIFICATION_KAFKA_BROKERS", ""}, {"TASK_NOTIFICATION_KAFKA_BROKERS", "kafka"}, {"TASK_NOTIFICATION_KAFKA_BROKERS", ":9092"},
		{"TASK_NOTIFICATION_KAFKA_BROKERS", "kafka:0"}, {"TASK_NOTIFICATION_KAFKA_BROKERS", "kafka:65536"},
		{"TASK_NOTIFICATION_KAFKA_BROKERS", "http://secret:9092"}, {"TASK_NOTIFICATION_KAFKA_BROKERS", "kafka:9092,"},
		{"TASK_NOTIFICATION_CHAT_TOPIC", "task_notices"}, {"TASK_NOTIFICATION_AGENT_TOPIC", "task_notices"},
		{"TASK_NOTIFICATION_CHAT_TOPIC", "bad topic"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			env := map[string]string{"TASK_NOTIFICATION_PUBLISH_ENABLED": "true", "TASK_NOTIFICATION_TOPIC": "task_notices", "TASK_NOTIFICATION_KAFKA_BROKERS": "kafka:19092"}
			env[tc.key] = tc.value
			_, err := loadTaskNotificationPublishConfig(notificationEnvironment(env))
			if err == nil {
				t.Fatal("unsafe enabled config accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed input")
			}
		})
	}
}

func TestTaskNotificationPublishConfigLoadsIPv6AndPrivateReservedTopics(t *testing.T) {
	cfg, err := loadTaskNotificationPublishConfig(notificationEnvironment(map[string]string{
		"TASK_NOTIFICATION_PUBLISH_ENABLED": "true", "TASK_NOTIFICATION_TOPIC": "task_notices", "TASK_NOTIFICATION_KAFKA_BROKERS": " kafka:19092, [::1]:9092 ",
		"TASK_NOTIFICATION_CHAT_TOPIC": "private_chat", "TASK_NOTIFICATION_AGENT_TOPIC": "private_agent",
	}))
	if err != nil || !cfg.Enabled || len(cfg.Brokers) != 2 || cfg.Brokers[0] != "kafka:19092" || cfg.Brokers[1] != "[::1]:9092" {
		t.Fatalf("config: %#v %v", cfg, err)
	}
	called := false
	_, err = newTaskNotificationRuntime(nil, cfg, nil, func(*gorm.DB, taskNotificationPublishConfig, *zap.Logger) taskNotificationRuntime {
		called = true
		return taskNotificationRuntime{}
	})
	if err == nil || called {
		t.Fatal("enabled runtime must require Task-owned database before constructing Kafka")
	}
}

type notificationRuntimeRunner struct{ started, cancelled, release, finished chan struct{} }

func (r notificationRuntimeRunner) Start(ctx context.Context) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	<-r.release
	close(r.finished)
}

type notificationRuntimeCloser struct {
	closed   chan struct{}
	finished <-chan struct{}
	calls    int
	err      error
	t        *testing.T
}

func (c *notificationRuntimeCloser) Close() error {
	select {
	case <-c.finished:
	default:
		c.t.Error("writer closed before publisher stopped")
	}
	c.calls++
	close(c.closed)
	return c.err
}
func awaitNotificationRuntime(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime operation timed out")
	}
}

func TestTaskNotificationRuntimeCancelsWaitsThenClosesOnlyOnce(t *testing.T) {
	runner := notificationRuntimeRunner{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	closer := &notificationRuntimeCloser{closed: make(chan struct{}), finished: runner.finished, err: errors.New("secret broker address"), t: t}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(runner.release) }) })
	runtime, err := newTaskNotificationRuntime(&gorm.DB{}, taskNotificationPublishConfig{Enabled: true}, nil,
		func(*gorm.DB, taskNotificationPublishConfig, *zap.Logger) taskNotificationRuntime {
			return taskNotificationRuntime{runner: runner, writer: closer}
		})
	if err != nil {
		t.Fatal(err)
	}
	stop := startTaskNotificationPublisher(context.Background(), runtime)
	awaitNotificationRuntime(t, runner.started)
	result := make(chan error, 1)
	go func() { result <- stop() }()
	awaitNotificationRuntime(t, runner.cancelled)
	select {
	case <-closer.closed:
		t.Fatal("close happened while publisher still running")
	default:
	}
	releaseOnce.Do(func() { close(runner.release) })
	select {
	case err := <-result:
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe close result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop failed")
	}
	if err := stop(); err == nil || closer.calls != 1 {
		t.Fatalf("repeat stop: %v, closes=%d", err, closer.calls)
	}
}

func TestTaskNotificationKafkaWriterUsesSynchronousAllAcknowledgements(t *testing.T) {
	w := newTaskNotificationKafkaWriter([]string{"localhost:9092"}, "task_notices")
	if w.Async || w.RequiredAcks != -1 || w.MaxAttempts != 1 || w.WriteTimeout != 5*time.Second {
		t.Fatal("unsafe Kafka publication settings")
	}
	// Construction has not dialled a broker. Closing requires no network.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
