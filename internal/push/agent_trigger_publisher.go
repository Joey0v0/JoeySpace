package push

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
)

const agentTriggerBatchLimit = 100

// AgentTriggerWriter acknowledges notifications synchronously. Its owner must
// stop the publisher before closing the writer.
type AgentTriggerWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
	Close() error
}

func NewKafkaAgentTriggerWriter(brokers []string, topic string) AgentTriggerWriter {
	return &kafka.Writer{Addr: kafka.TCP(brokers...), Topic: topic, Balancer: &kafka.Hash{},
		RequiredAcks: kafka.RequireAll, Async: false, MaxAttempts: 1,
		WriteTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, BatchTimeout: 10 * time.Millisecond}
}

type AgentTriggerPublisher struct {
	store        repository.AgentTriggerStore
	writer       AgentTriggerWriter
	logger       *zap.Logger
	roundTimeout time.Duration
	writeTimeout time.Duration
	pollInterval time.Duration
}

func NewAgentTriggerPublisher(store repository.AgentTriggerStore, writer AgentTriggerWriter, logger *zap.Logger) *AgentTriggerPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AgentTriggerPublisher{store: store, writer: writer, logger: logger,
		roundTimeout: 10 * time.Second, writeTimeout: 5 * time.Second, pollInterval: time.Second}
}

// RunOnce never holds a database lock while waiting for Kafka. An uncertain
// acknowledgement or failed published update leaves the same event retryable.
func (p *AgentTriggerPublisher) RunOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.store == nil || p.writer == nil {
		return errors.New("agent trigger publisher is not configured")
	}
	roundCtx, cancel := context.WithTimeout(ctx, p.roundTimeout)
	defer cancel()
	rows, err := p.store.ListPending(roundCtx, agentTriggerBatchLimit)
	if roundCtx.Err() != nil {
		return roundCtx.Err()
	}
	if err != nil {
		return errors.New("agent trigger pending read failed")
	}
	if len(rows) > agentTriggerBatchLimit {
		return errors.New("agent trigger pending batch exceeds limit")
	}
	for _, row := range rows {
		if err := roundCtx.Err(); err != nil {
			return err
		}
		if row.Published || row.Validate() != nil {
			return errors.New("agent trigger pending record is invalid")
		}
		body, err := json.Marshal(row.Event())
		if err != nil {
			return errors.New("agent trigger notification encoding failed")
		}
		writeCtx, stopWrite := context.WithTimeout(roundCtx, p.writeTimeout)
		err = p.writer.WriteMessages(writeCtx, kafka.Message{Key: []byte(row.RequestKey()), Value: body})
		writeContextErr := writeCtx.Err()
		stopWrite()
		if writeContextErr != nil {
			return writeContextErr
		}
		if err != nil {
			return errors.New("agent trigger Kafka acknowledgement failed")
		}
		if err := roundCtx.Err(); err != nil {
			return err
		}
		if err := p.store.MarkPublished(roundCtx, row); err != nil {
			if roundCtx.Err() != nil {
				return roundCtx.Err()
			}
			return errors.New("agent trigger published update failed")
		}
		if err := roundCtx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Start publishes immediately, then waits between rounds even after failure.
// The process owns cancellation, waiting for this method, and writer.Close.
func (p *AgentTriggerPublisher) Start(ctx context.Context) {
	if p == nil {
		return
	}
	logger := p.logger
	if logger == nil {
		logger = zap.NewNop()
	}
	logger.Info("agent trigger publisher started")
	defer logger.Info("agent trigger publisher stopped")
	for ctx.Err() == nil {
		if err := p.RunOnce(ctx); err != nil && ctx.Err() == nil {
			// RunOnce returns safe stage errors, never raw broker/SQL details.
			logger.Warn("agent trigger publication failed; pending events remain retryable", zap.Error(err))
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(p.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
