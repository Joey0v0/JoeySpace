package main

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/push"
	"github.com/yjydist/go-im/internal/rpcauth"
	"go.uber.org/zap"
)

type notificationRuntimeSender interface {
	push.TaskNotificationSender
	Close() error
}

type taskNotificationPushFactories struct {
	client func(push.TaskNotificationClientConfig) (notificationRuntimeSender, error)
	reader func(kafka.ReaderConfig) push.TaskNotificationReader
}

type taskNotificationPushRuntime struct {
	reader push.TaskNotificationReader
	client notificationRuntimeSender
	topic  string
	logger *zap.Logger

	mu        sync.Mutex
	started   bool
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func nilNotificationRuntimeResource(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Chan, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func prepareTaskNotificationPush(cfg *config.Config, logger *zap.Logger, factories taskNotificationPushFactories) (*taskNotificationPushRuntime, error) {
	if err := config.ValidateTaskNotificationPush(cfg); err != nil {
		return nil, err
	}
	c := cfg.TaskNotifications.Push
	if !c.Enabled {
		return nil, nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	clientFactory := factories.client
	if clientFactory == nil {
		clientFactory = func(c push.TaskNotificationClientConfig) (notificationRuntimeSender, error) {
			return push.NewTaskNotificationClient(c)
		}
	}
	readerFactory := factories.reader
	if readerFactory == nil {
		readerFactory = func(c kafka.ReaderConfig) push.TaskNotificationReader { return kafka.NewReader(c) }
	}
	routes := make(map[string]string, len(c.Routes))
	for address, origin := range c.Routes {
		routes[address] = origin
	}
	readerConfig := kafka.ReaderConfig{Brokers: append([]string(nil), cfg.Kafka.Brokers...), GroupID: c.ConsumerGroup, Topic: c.Topic,
		CommitInterval: 0, StartOffset: kafka.FirstOffset, MinBytes: 1, MaxBytes: 1e6, QueueCapacity: 1,
		WatchPartitionChanges: true}
	if err := readerConfig.Validate(); err != nil {
		return nil, errors.New("invalid task notification reader configuration")
	}
	client, err := clientFactory(push.TaskNotificationClientConfig{Files: rpcauth.CertificateFiles{
		CertFile: c.TLS.CertFile, KeyFile: c.TLS.KeyFile, CAFile: c.TLS.CAFile}, WSDNSName: c.WSDNSName, Routes: routes})
	if err != nil || nilNotificationRuntimeResource(client) {
		if !nilNotificationRuntimeResource(client) {
			_ = client.Close()
		}
		return nil, errors.New("cannot prepare task notification TLS client")
	}
	reader := readerFactory(readerConfig)
	if nilNotificationRuntimeResource(reader) {
		_ = client.Close()
		return nil, errors.New("cannot prepare task notification reader")
	}
	return &taskNotificationPushRuntime{reader: reader, client: client, topic: c.Topic, logger: logger}, nil
}

func (r *taskNotificationPushRuntime) Start(ctx context.Context, online push.TaskNotificationOnlineLocator) error {
	if r == nil {
		return errors.New("task notification runtime unavailable")
	}
	if ctx == nil {
		return errors.New("task notification runtime context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.started {
		return errors.New("task notification runtime cannot start again")
	}
	consumer, err := push.NewTaskNotificationConsumer(r.reader, online, r.client, r.topic, r.logger)
	if err != nil {
		return errors.New("cannot start task notification consumer")
	}
	workerCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.started = true
	go func() {
		defer close(r.done)
		if err := consumer.Run(workerCtx); errors.Is(err, push.ErrInvalidTaskNotification) {
			r.logger.Error("task notification consumer stopped; invalid event remains uncommitted")
		} else if err != nil && workerCtx.Err() == nil {
			r.logger.Error("task notification consumer stopped unexpectedly")
		}
	}()
	return nil
}

// Close owns cancellation and waits for all notification I/O before resources.
// It does not depend on shutdown of the independent chat worker.
func (r *taskNotificationPushRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		cancel, done := r.cancel, r.done
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		if !nilNotificationRuntimeResource(r.reader) && r.reader.Close() != nil {
			r.closeErr = errors.New("cannot close task notification reader")
		}
		if !nilNotificationRuntimeResource(r.client) && r.client.Close() != nil {
			r.closeErr = errors.Join(r.closeErr, errors.New("cannot close task notification TLS client"))
		}
	})
	return r.closeErr
}
