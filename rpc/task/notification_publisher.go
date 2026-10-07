package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

const taskNotificationPublishBatchLimit = 100

var (
	errTaskNotificationPublishUnavailable = errors.New("task notification publisher unavailable")
	errTaskNotificationPublishList        = errors.New("task notification outbox list failed")
	errTaskNotificationPublishBatch       = errors.New("invalid task notification outbox batch")
	errTaskNotificationPublishEncode      = errors.New("task notification event encode failed")
	errTaskNotificationPublishWrite       = errors.New("task notification event write failed")
	errTaskNotificationPublishMark        = errors.New("task notification outbox mark failed")
	errTaskNotificationPublishContext     = errors.New("task notification publish context ended")
)

type taskNotificationPublishRowError struct {
	notificationID int64
	cause          error
}

func (e *taskNotificationPublishRowError) Error() string { return e.cause.Error() }
func (e *taskNotificationPublishRowError) Unwrap() error { return e.cause }

type taskNotificationPublisher struct {
	store        taskNotificationOutboxStore
	writer       taskNotificationWriter
	logger       *zap.Logger
	roundTimeout time.Duration
	writeTimeout time.Duration
	interval     time.Duration
}

// Construction is side-effect free; runtime ownership closes the writer after Start exits.
func newTaskNotificationPublisher(store taskNotificationOutboxStore, writer taskNotificationWriter, logger *zap.Logger) *taskNotificationPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &taskNotificationPublisher{
		store:        store,
		writer:       writer,
		logger:       logger,
		roundTimeout: 10 * time.Second,
		writeTimeout: 5 * time.Second,
		interval:     time.Second,
	}
}

func (p *taskNotificationPublisher) RunOnce(ctx context.Context) error {
	if p == nil || p.store == nil || p.writer == nil || ctx == nil {
		return errTaskNotificationPublishUnavailable
	}
	if ctx.Err() != nil {
		return errTaskNotificationPublishContext
	}
	roundTimeout := p.roundTimeout
	if roundTimeout <= 0 || roundTimeout > 10*time.Second {
		roundTimeout = 10 * time.Second
	}
	writeTimeout := p.writeTimeout
	if writeTimeout <= 0 || writeTimeout > 5*time.Second {
		writeTimeout = 5 * time.Second
	}
	roundCtx, cancel := context.WithTimeout(ctx, roundTimeout)
	defer cancel()
	rows, err := p.store.ListPending(roundCtx, taskNotificationPublishBatchLimit)
	if err != nil {
		return errTaskNotificationPublishList
	}
	if roundCtx.Err() != nil {
		return errTaskNotificationPublishContext
	}
	if len(rows) > taskNotificationPublishBatchLimit {
		return errTaskNotificationPublishBatch
	}
	var previousID int64
	for _, row := range rows {
		if row.Published || row.Validate() != nil || row.NotificationID <= previousID {
			return errTaskNotificationPublishBatch
		}
		previousID = row.NotificationID
	}
	for _, row := range rows {
		if roundCtx.Err() != nil {
			return &taskNotificationPublishRowError{row.NotificationID, errTaskNotificationPublishContext}
		}
		event := row.Event()
		payload, err := json.Marshal(event)
		if err != nil {
			return &taskNotificationPublishRowError{row.NotificationID, errTaskNotificationPublishEncode}
		}
		writeCtx, writeCancel := context.WithTimeout(roundCtx, writeTimeout)
		err = p.writer.WriteMessages(writeCtx, kafka.Message{
			Key:   []byte(event.Key()),
			Value: payload,
		})
		writeEnded := writeCtx.Err() != nil || roundCtx.Err() != nil
		writeCancel()
		if err != nil {
			return &taskNotificationPublishRowError{row.NotificationID, errTaskNotificationPublishWrite}
		}
		if writeEnded {
			return &taskNotificationPublishRowError{row.NotificationID, errTaskNotificationPublishContext}
		}
		if err := p.store.MarkPublished(roundCtx, row); err != nil {
			return &taskNotificationPublishRowError{row.NotificationID, errTaskNotificationPublishMark}
		}
		if roundCtx.Err() != nil {
			return &taskNotificationPublishRowError{row.NotificationID, errTaskNotificationPublishContext}
		}
	}
	return nil
}

// Start retries after a bounded pause; cancellation interrupts both work and waiting.
func (p *taskNotificationPublisher) Start(ctx context.Context) {
	if p == nil || ctx == nil {
		return
	}
	for ctx.Err() == nil {
		if err := p.RunOnce(ctx); err != nil && ctx.Err() == nil && p.logger != nil {
			fields := []zap.Field{zap.String("phase", err.Error())}
			var rowErr *taskNotificationPublishRowError
			if errors.As(err, &rowErr) {
				fields = append(fields, zap.Int64("notification_id", rowErr.notificationID))
			}
			p.logger.Warn("task notification publish round failed", fields...)
		}
		interval := p.interval
		if interval <= 0 {
			interval = time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
