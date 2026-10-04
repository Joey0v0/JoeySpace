package main

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type botKafkaWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
}

type botCardPublisher interface {
	Publish(context.Context, botSendIntent) (string, error)
}

type kafkaBotPublisher struct {
	store  botSendStore
	writer botKafkaWriter
}

func (p *kafkaBotPublisher) Publish(ctx context.Context, intent botSendIntent) (string, error) {
	if p.store == nil || p.writer == nil {
		return "", status.Error(codes.Unavailable, "bot sending is not enabled")
	}
	record, err := p.store.Prepare(ctx, intent)
	if err != nil {
		return "", err
	}
	// Validate even accepted rows before the replay fast path. A store result
	// must describe this exact item, never another item's accepted message.
	if !record.sameIntent(intent) || record.BotID <= 0 || record.InitiatorID <= 0 || record.TeamID <= 0 ||
		record.GroupID <= 0 || record.TimestampUnixMs <= 0 {
		return "", status.Error(codes.Internal, "invalid stored bot send")
	}
	if _, err := model.DecodeTaskCreatedCard(record.Content); err != nil {
		return "", status.Error(codes.Internal, "invalid stored bot send")
	}
	if record.Accepted {
		return record.MsgID, nil
	}
	event := model.ChatEvent{MsgID: record.MsgID, FromID: record.BotID, SenderType: model.MessageSenderBot,
		InitiatorID: record.InitiatorID, ToID: record.GroupID, ChatType: 2, ContentType: int(model.MessageContentTaskCard),
		Content: record.Content, Timestamp: record.TimestampUnixMs}
	body, err := json.Marshal(event)
	if err != nil {
		return "", status.Error(codes.Internal, "cannot encode bot message")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = p.writer.WriteMessages(writeCtx, kafka.Message{Key: []byte(strconv.FormatInt(record.GroupID, 10)), Value: body})
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return "", status.FromContextError(ctx.Err()).Err()
		}
		return "", status.Error(codes.Unavailable, "bot send result uncertain; retry the same run")
	}
	if err := p.store.MarkAccepted(ctx, record.MsgID); err != nil {
		return "", err
	}
	return record.MsgID, nil
}

func newBotKafkaWriter(brokers []string, topic string) *kafka.Writer {
	return &kafka.Writer{Addr: kafka.TCP(brokers...), Topic: topic, RequiredAcks: kafka.RequireAll,
		Async: false, MaxAttempts: 1, Balancer: &kafka.Hash{},
		WriteTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, BatchTimeout: 10 * time.Millisecond}
}
