package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type TriggerConsumer struct {
	reader     TriggerEventReader
	inbox      TriggerInbox
	retryDelay time.Duration
	closeOnce  sync.Once
	closeErr   error
	closed     atomic.Bool
	running    atomic.Bool
}

func nilTriggerConsumerDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func NewTriggerConsumer(reader TriggerEventReader, inbox TriggerInbox) (*TriggerConsumer, error) {
	if nilTriggerConsumerDependency(reader) || nilTriggerConsumerDependency(inbox) {
		return nil, errors.New("trigger notification reader and inbox are required")
	}
	return &TriggerConsumer{reader: reader, inbox: inbox, retryDelay: time.Second}, nil
}

func (c *TriggerConsumer) check(ctx context.Context) error {
	if ctx == nil {
		return errors.New("trigger notification context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.reader == nil || c.inbox == nil || c.closed.Load() {
		return errors.New("trigger notification consumer is unavailable")
	}
	return nil
}

func (c *TriggerConsumer) waitRetry(ctx context.Context) error {
	if err := c.check(ctx); err != nil {
		return err
	}
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

// Run never fetches a higher offset until the current notification is both
// durable and acknowledged. Its owner cancels and waits before calling Close.
func (c *TriggerConsumer) Run(ctx context.Context) error {
	if err := c.check(ctx); err != nil {
		return err
	}
	if !c.running.CompareAndSwap(false, true) {
		return errors.New("trigger notification consumer is already running")
	}
	defer c.running.Store(false)
	for {
		if err := c.check(ctx); err != nil {
			return err
		}
		message, err := c.reader.FetchMessage(ctx)
		if contextErr := c.check(ctx); contextErr != nil {
			return contextErr
		}
		if err != nil {
			if err := c.waitRetry(ctx); err != nil {
				return err
			}
			continue
		}
		event, err := DecodeTriggerNotification(message)
		if contextErr := c.check(ctx); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return ErrInvalidTriggerNotification
		}
		for {
			if err := c.check(ctx); err != nil {
				return err
			}
			err := c.inbox.Accept(ctx, event)
			if contextErr := c.check(ctx); contextErr != nil {
				return contextErr
			}
			if status.Code(err) == codes.FailedPrecondition {
				return ErrInvalidTriggerInbox
			}
			if err == nil {
				break
			}
			if err := c.waitRetry(ctx); err != nil {
				return err
			}
		}
		for {
			if err := c.check(ctx); err != nil {
				return err
			}
			commitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := c.reader.CommitMessages(commitCtx, message)
			commitContextErr := commitCtx.Err()
			cancel()
			if contextErr := c.check(ctx); contextErr != nil {
				return contextErr
			}
			if err == nil && commitContextErr == nil {
				break
			}
			if err := c.waitRetry(ctx); err != nil {
				return err
			}
		}
	}
}

func (c *TriggerConsumer) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.reader != nil && c.reader.Close() != nil {
			c.closeErr = errors.New("cannot close trigger notification reader")
		}
	})
	return c.closeErr
}
