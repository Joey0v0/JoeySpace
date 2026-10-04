package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/push"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func validateAgentTriggerConfig(cfg config.KafkaConfig) error {
	if !cfg.AgentTriggerEnabled {
		return nil
	}
	if len(cfg.Brokers) == 0 {
		return errors.New("agent trigger requires Kafka brokers")
	}
	for _, broker := range cfg.Brokers {
		if strings.TrimSpace(broker) == "" {
			return errors.New("agent trigger Kafka broker cannot be blank")
		}
	}
	for _, topic := range []string{cfg.TopicChat, cfg.TopicAgentTrigger} {
		if topic == "" || strings.IndexFunc(topic, unicode.IsSpace) >= 0 {
			return errors.New("agent trigger and chat topics must be nonempty and contain no whitespace")
		}
	}
	if cfg.TopicAgentTrigger == cfg.TopicChat {
		return errors.New("agent trigger topic must be different from chat topic")
	}
	return nil
}

type pushRunner interface {
	Start(context.Context)
}

type pushConsumer interface {
	pushRunner
	Close() error
}

type pushMessaging struct {
	messages  repository.MessageRepository
	publisher pushRunner
	writer    io.Closer
}

// Only the selected factory is called. Disabled deployments do not construct an
// Outbox store or writer, and retain the original message repository.
type agentTriggerFactories struct {
	legacy  func() repository.MessageRepository
	enabled func(*gorm.DB, []string, string, *zap.Logger) pushMessaging
}

func newAgentTriggerMessaging(db *gorm.DB, brokers []string, topic string, logger *zap.Logger) pushMessaging {
	writer := push.NewKafkaAgentTriggerWriter(brokers, topic)
	store := repository.NewAgentTriggerStore(db)
	return pushMessaging{
		messages:  repository.NewAgentTriggerMessageRepository(db),
		publisher: push.NewAgentTriggerPublisher(store, writer, logger),
		writer:    writer,
	}
}

func newPushMessaging(cfg config.KafkaConfig, db *gorm.DB, logger *zap.Logger, factories agentTriggerFactories) (pushMessaging, error) {
	if err := validateAgentTriggerConfig(cfg); err != nil {
		return pushMessaging{}, err
	}
	if !cfg.AgentTriggerEnabled {
		factory := factories.legacy
		if factory == nil {
			factory = repository.NewMessageRepository
		}
		return pushMessaging{messages: factory()}, nil
	}
	factory := factories.enabled
	if factory == nil {
		factory = newAgentTriggerMessaging
	}
	brokers := make([]string, len(cfg.Brokers))
	for i, broker := range cfg.Brokers {
		brokers[i] = strings.TrimSpace(broker)
	}
	return factory(db, brokers, cfg.TopicAgentTrigger, logger), nil
}

// The caller cancels ctx before waiting. Both loops finish before either Kafka
// resource is closed, including the original consumer when Outbox is disabled.
func startPushWorkers(ctx context.Context, consumer pushConsumer, publisher pushRunner, writer io.Closer) func() error {
	var workers sync.WaitGroup
	start := func(runner pushRunner) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			runner.Start(ctx)
		}()
	}
	start(consumer)
	if publisher != nil {
		start(publisher)
	}
	return func() error {
		workers.Wait()
		consumerErr := consumer.Close()
		if writer != nil {
			return errors.Join(consumerErr, writer.Close())
		}
		return consumerErr
	}
}
