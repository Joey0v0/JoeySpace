package push

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"go.uber.org/zap"
)

var ErrInvalidTaskNotification = errors.New("invalid task notification; consumption stopped without commit")

type TaskNotificationReader interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
	Close() error
}
type TaskNotificationOnlineLocator interface {
	GetOnline(context.Context, int64) (string, error)
}
type TaskNotificationSender interface {
	Send(context.Context, string, model.TaskNotificationEvent) (model.TaskNotificationDelivery, error)
}

type TaskNotificationConsumer struct {
	reader     TaskNotificationReader
	online     TaskNotificationOnlineLocator
	sender     TaskNotificationSender
	topic      string
	logger     *zap.Logger
	retryDelay time.Duration
	ioTimeout  time.Duration
	running    atomic.Bool
	halted     atomic.Bool
	closed     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
}

func nilTaskNotificationDependency(value any) bool {
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

func NewTaskNotificationConsumer(reader TaskNotificationReader, online TaskNotificationOnlineLocator, sender TaskNotificationSender, topic string, logger *zap.Logger) (*TaskNotificationConsumer, error) {
	if nilTaskNotificationDependency(reader) || nilTaskNotificationDependency(online) || nilTaskNotificationDependency(sender) || topic == "" || strings.TrimSpace(topic) != topic {
		return nil, errors.New("task notification consumer dependencies and topic required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TaskNotificationConsumer{reader: reader, online: online, sender: sender, topic: topic, logger: logger, retryDelay: time.Second, ioTimeout: 3 * time.Second}, nil
}

func (c *TaskNotificationConsumer) check(ctx context.Context) error {
	if ctx == nil {
		return errors.New("task notification consumer context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.reader == nil || c.online == nil || c.sender == nil || c.closed.Load() {
		return errors.New("task notification consumer unavailable")
	}
	if c.halted.Load() {
		return ErrInvalidTaskNotification
	}
	return nil
}

func (c *TaskNotificationConsumer) wait(ctx context.Context) error {
	delay := c.retryDelay
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return c.check(ctx)
	}
}

func (c *TaskNotificationConsumer) attempt(ctx context.Context, event model.TaskNotificationEvent) error {
	limit := c.ioTimeout
	if limit <= 0 || limit > 3*time.Second {
		limit = 3 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	addr, err := c.online.GetOnline(callCtx, event.RecipientID)
	if err != nil || callCtx.Err() != nil {
		return errors.New("task notification online lookup failed")
	}
	if addr == "" {
		return nil
	} // Explicitly offline: Task still owns the persistent notice.
	result, err := c.sender.Send(callCtx, addr, event)
	if err != nil || callCtx.Err() != nil || result.Validate(event.NotificationID) != nil {
		return errors.New("task notification delivery attempt failed")
	}
	return nil // queued, offline and denied are terminal hint outcomes under A72.
}

// Run never fetches a later message until the current hint outcome is committed.
// Invalid events latch the instance stopped; restarting requires a fresh reader.
func (c *TaskNotificationConsumer) Run(ctx context.Context) error {
	if err := c.check(ctx); err != nil {
		return err
	}
	if !c.running.CompareAndSwap(false, true) {
		return errors.New("task notification consumer already running")
	}
	defer c.running.Store(false)
	for {
		if err := c.check(ctx); err != nil {
			return err
		}
		message, err := c.reader.FetchMessage(ctx)
		if err := c.check(ctx); err != nil {
			return err
		}
		if err != nil {
			if err := c.wait(ctx); err != nil {
				return err
			}
			continue
		}
		event, err := model.DecodeTaskNotificationEvent(message.Value)
		if err != nil || message.Topic != c.topic || message.Partition < 0 || message.Offset < 0 || string(message.Key) != event.Key() {
			c.halted.Store(true)
			return ErrInvalidTaskNotification
		}
		for {
			if err := c.check(ctx); err != nil {
				return err
			}
			err := c.attempt(ctx, event)
			if err := c.check(ctx); err != nil {
				return err
			}
			if err == nil {
				break
			}
			c.logger.Warn("task notification delivery retry pending")
			if err := c.wait(ctx); err != nil {
				return err
			}
		}
		for {
			if err := c.check(ctx); err != nil {
				return err
			}
			commitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := c.reader.CommitMessages(commitCtx, message)
			ended := commitCtx.Err() != nil
			cancel()
			if err := c.check(ctx); err != nil {
				return err
			}
			if err == nil && !ended {
				break
			}
			c.logger.Warn("task notification offset commit retry pending")
			if err := c.wait(ctx); err != nil {
				return err
			}
		}
	}
}

// Its owner cancels and waits for Run before closing either reader or sender.
func (c *TaskNotificationConsumer) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.reader != nil && c.reader.Close() != nil {
			c.closeErr = errors.New("cannot close task notification reader")
		}
	})
	return c.closeErr
}
