package main

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/segmentio/kafka-go"
	agent "github.com/yjydist/go-im/rpc/agent"
	"gorm.io/gorm"
)

type agentTriggerInboxConfig struct {
	Enabled bool
	Brokers []string
	Topic   string
	Group   string
}

func loadAgentTriggerInboxConfig(getenv func(string) string) (agentTriggerInboxConfig, error) {
	if getenv == nil {
		return agentTriggerInboxConfig{}, errors.New("Agent trigger environment reader is required")
	}
	switch getenv("AGENT_TRIGGER_INBOX_ENABLED") {
	case "", "false":
		return agentTriggerInboxConfig{}, nil
	case "true":
	default:
		return agentTriggerInboxConfig{}, errors.New("invalid Agent trigger inbox switch")
	}
	cfg := agentTriggerInboxConfig{Enabled: true, Brokers: strings.Split(getenv("AGENT_TRIGGER_KAFKA_BROKERS"), ","),
		Topic: getenv("AGENT_TRIGGER_KAFKA_TOPIC"), Group: getenv("AGENT_TRIGGER_KAFKA_GROUP")}
	if err := validateAgentTriggerInboxConfig(cfg); err != nil {
		return agentTriggerInboxConfig{}, err
	}
	return cfg, nil
}

func validateAgentTriggerInboxConfig(cfg agentTriggerInboxConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if len(cfg.Brokers) == 0 {
		return errors.New("Agent trigger Kafka brokers are required")
	}
	for _, broker := range cfg.Brokers {
		host, portText, err := net.SplitHostPort(broker)
		port, portErr := strconv.Atoi(portText)
		if err != nil || host == "" || !utf8.ValidString(broker) || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText ||
			strings.ContainsAny(host, "*/\\?#@") || strings.IndexFunc(broker, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
			strings.HasPrefix(broker, "[") && net.ParseIP(host) == nil {
			return errors.New("invalid Agent trigger Kafka broker")
		}
	}
	if cfg.Topic == "" || len(cfg.Topic) > 249 || cfg.Topic == "." || cfg.Topic == ".." || cfg.Topic == "chat_messages" ||
		strings.IndexFunc(cfg.Topic, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
		}) >= 0 {
		return errors.New("dedicated Agent trigger Kafka topic is required")
	}
	if cfg.Group == "" || !utf8.ValidString(cfg.Group) || cfg.Group == "im_push_group" || strings.IndexFunc(cfg.Group, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("dedicated Agent trigger Kafka group is required")
	}
	return nil
}

type agentTriggerReaderFactory func(kafka.ReaderConfig) (agent.TriggerEventReader, error)

type agentTriggerInboxConsumer interface {
	Run(context.Context) error
	Close() error
}

func closePreparedAgentTriggerReader(reader agent.TriggerEventReader) {
	if reader == nil {
		return
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return
		}
	}
	_ = reader.Close()
}

// This runtime owns only notification consumption. Its errors never stop the
// ordinary Agent RPC server and it does not read context or invoke a model.
type agentTriggerInbox struct {
	consumer  agentTriggerInboxConsumer
	mu        sync.Mutex
	started   bool
	stopped   bool
	cancel    context.CancelFunc
	done      chan struct{}
	runError  error
	closeOnce sync.Once
	closeErr  error
}

func newAgentTriggerInbox(cfg agentTriggerInboxConfig, db *gorm.DB, factory agentTriggerReaderFactory) (*agentTriggerInbox, error) {
	if err := validateAgentTriggerInboxConfig(cfg); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("Agent trigger inbox database is required")
	}
	if factory == nil {
		factory = func(config kafka.ReaderConfig) (agent.TriggerEventReader, error) { return kafka.NewReader(config), nil }
	}
	reader, err := factory(kafka.ReaderConfig{Brokers: append([]string(nil), cfg.Brokers...), Topic: cfg.Topic,
		GroupID: cfg.Group, CommitInterval: 0, MinBytes: 1, MaxBytes: 64 * 1024, StartOffset: kafka.FirstOffset})
	if err != nil {
		closePreparedAgentTriggerReader(reader)
		return nil, errors.New("cannot prepare Agent trigger Kafka reader")
	}
	consumer, err := agent.NewTriggerConsumer(reader, agent.NewTriggerInboxStore(db))
	if err != nil {
		closePreparedAgentTriggerReader(reader)
		return nil, errors.New("cannot prepare Agent trigger inbox consumer")
	}
	return &agentTriggerInbox{consumer: consumer}, nil
}

func safeAgentTriggerInboxFailure(err error) error {
	switch {
	case errors.Is(err, agent.ErrInvalidTriggerNotification):
		return errors.New("invalid Agent trigger notification; consumer stopped without acknowledgement, repair and restart required")
	case errors.Is(err, agent.ErrInvalidTriggerInbox):
		return errors.New("invalid saved Agent trigger fact; consumer stopped without acknowledgement, repair and restart required")
	default:
		return errors.New("Agent trigger consumer stopped; check configuration and restart after repair")
	}
}

func (inbox *agentTriggerInbox) Start(parent context.Context, report func(error)) error {
	if inbox == nil {
		return nil
	}
	if parent == nil || inbox.consumer == nil {
		return errors.New("Agent trigger inbox context and consumer are required")
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	if inbox.started || inbox.stopped {
		return errors.New("Agent trigger inbox cannot be started again")
	}
	ctx, cancel := context.WithCancel(parent)
	inbox.started, inbox.cancel, inbox.done = true, cancel, make(chan struct{})
	go func() {
		defer close(inbox.done)
		err := inbox.consumer.Run(ctx)
		if ctx.Err() == nil {
			failure := safeAgentTriggerInboxFailure(err)
			inbox.mu.Lock()
			inbox.runError = failure
			inbox.mu.Unlock()
			if report != nil {
				report(failure)
			}
		}
	}()
	return nil
}

func (inbox *agentTriggerInbox) stoppedError() error {
	if inbox == nil {
		return nil
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return inbox.runError
}

func (inbox *agentTriggerInbox) Stop() error {
	if inbox == nil {
		return nil
	}
	inbox.mu.Lock()
	inbox.stopped = true
	cancel, done := inbox.cancel, inbox.done
	inbox.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	inbox.closeOnce.Do(func() {
		if inbox.consumer != nil {
			if err := inbox.consumer.Close(); err != nil {
				inbox.closeErr = errors.New("cannot close Agent trigger Kafka reader")
			}
		}
	})
	return inbox.closeErr
}
