package main

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type taskNotificationPublishConfig struct {
	Enabled bool
	Brokers []string
	Topic   string
}

var taskNotificationTopicName = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,249}$`)
var taskNotificationBrokerHost = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func loadTaskNotificationPublishConfig(getenv func(string) string) (taskNotificationPublishConfig, error) {
	var c taskNotificationPublishConfig
	value := strings.TrimSpace(getenv("TASK_NOTIFICATION_PUBLISH_ENABLED"))
	if value == "" || value == "false" {
		return c, nil
	}
	if value != "true" {
		return c, errors.New("TASK_NOTIFICATION_PUBLISH_ENABLED must be true or false")
	}
	c.Enabled = true
	c.Topic = strings.TrimSpace(getenv("TASK_NOTIFICATION_TOPIC"))
	if !validTaskNotificationTopic(c.Topic) {
		return c, errors.New("invalid task notification topic")
	}
	for key, fallback := range map[string]string{"TASK_NOTIFICATION_CHAT_TOPIC": "chat_messages", "TASK_NOTIFICATION_AGENT_TOPIC": "agent_task_triggers"} {
		reserved := strings.TrimSpace(getenv(key))
		if reserved == "" {
			reserved = fallback
		}
		if !validTaskNotificationTopic(reserved) || c.Topic == reserved {
			return c, errors.New("task notification topic must be separate from chat and Agent topics")
		}
	}
	for _, raw := range strings.Split(getenv("TASK_NOTIFICATION_KAFKA_BROKERS"), ",") {
		broker := strings.TrimSpace(raw)
		host, port, err := net.SplitHostPort(broker)
		portNumber, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || host == "" || (net.ParseIP(host) == nil && !taskNotificationBrokerHost.MatchString(host)) || portErr != nil || portNumber == 0 {
			return c, errors.New("invalid task notification Kafka brokers")
		}
		c.Brokers = append(c.Brokers, broker)
	}
	return c, nil
}

func validTaskNotificationTopic(topic string) bool {
	return taskNotificationTopicName.MatchString(topic) && topic != "." && topic != ".."
}

type taskNotificationRunner interface{ Start(context.Context) }
type taskNotificationRuntime struct {
	runner taskNotificationRunner
	writer io.Closer
}
type taskNotificationRuntimeFactory func(*gorm.DB, taskNotificationPublishConfig, *zap.Logger) taskNotificationRuntime

func newTaskNotificationRuntime(db *gorm.DB, cfg taskNotificationPublishConfig, logger *zap.Logger, factory taskNotificationRuntimeFactory) (taskNotificationRuntime, error) {
	if !cfg.Enabled {
		return taskNotificationRuntime{}, nil
	}
	if db == nil {
		return taskNotificationRuntime{}, errors.New("task notification storage is required")
	}
	if factory != nil {
		return factory(db, cfg, logger), nil
	}
	writer := newTaskNotificationKafkaWriter(cfg.Brokers, cfg.Topic)
	return taskNotificationRuntime{runner: newTaskNotificationPublisher(newTaskNotificationOutboxStore(db), writer, logger), writer: writer}, nil
}

func newTaskNotificationKafkaWriter(brokers []string, topic string) *kafka.Writer {
	return &kafka.Writer{Addr: kafka.TCP(brokers...), Topic: topic, Balancer: &kafka.Hash{}, RequiredAcks: kafka.RequireAll,
		Async: false, MaxAttempts: 1, WriteTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, BatchTimeout: 10 * time.Millisecond}
}

// Stop owns cancellation and waits before closing the writer; repeat calls are safe.
func startTaskNotificationPublisher(parent context.Context, runtime taskNotificationRuntime) func() error {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	if runtime.runner == nil {
		close(done)
	} else {
		go func() { defer close(done); runtime.runner.Start(ctx) }()
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			cancel()
			<-done
			if runtime.writer != nil && runtime.writer.Close() != nil {
				closeErr = errors.New("task notification writer close failed")
			}
		})
		return closeErr
	}
}
