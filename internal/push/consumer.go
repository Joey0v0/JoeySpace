package push

import (
	"context"
	"encoding/json"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/ws"
	"go.uber.org/zap"
)

// Consumer Kafka 消费者
type Consumer struct {
	reader     messageReader
	pusher     messagePusher
	logger     *zap.Logger
	retryDelay time.Duration
}

type messageReader interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
	Close() error
}

type messagePusher interface {
	HandleMessage(context.Context, *ws.KafkaChatMsg) error
}

// NewConsumer 创建 Kafka 消费者
func NewConsumer(brokers []string, topic, groupID string, pusher *Pusher, logger *zap.Logger) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:               brokers,
		Topic:                 topic,
		GroupID:               groupID,
		MinBytes:              1,
		MaxBytes:              10e6, // 10MB
		WatchPartitionChanges: true, // The topic may be auto-created after this reader joins.
	})

	return &Consumer{
		reader:     reader,
		pusher:     pusher,
		logger:     logger,
		retryDelay: time.Second,
	}
}

// Start 启动消费循环
func (c *Consumer) Start(ctx context.Context) {
	c.logger.Info("kafka consumer started")

	for ctx.Err() == nil {
		// FetchMessage 不会提前提交消费偏移量。
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			c.logger.Error("fetch kafka message failed", zap.Error(err))
			if !c.waitRetry(ctx) {
				break
			}
			continue
		}

		var chatMsg ws.KafkaChatMsg
		if err := json.Unmarshal(msg.Value, &chatMsg); err != nil {
			// 无法解析的消息无法通过原有处理逻辑恢复；记录并跳过它。
			c.logger.Error("unmarshal kafka message failed", zap.Error(err), zap.Int64("offset", msg.Offset))
		} else {
			c.logger.Info("received kafka message",
				zap.String("msg_id", chatMsg.MsgID),
				zap.Int64("from_id", chatMsg.FromID),
				zap.Int64("to_id", chatMsg.ToID),
				zap.Int("chat_type", chatMsg.ChatType),
			)
			for ctx.Err() == nil {
				if err := c.pusher.HandleMessage(ctx, &chatMsg); err == nil {
					break
				} else {
					c.logger.Error("handle message failed", zap.String("msg_id", chatMsg.MsgID), zap.Error(err))
				}
				if !c.waitRetry(ctx) {
					return
				}
			}
			if ctx.Err() != nil {
				break
			}
		}

		// 只在处理成功后提交；提交失败时只重试提交，不重复推送。
		for ctx.Err() == nil {
			if err := c.reader.CommitMessages(ctx, msg); err == nil {
				break
			} else {
				c.logger.Error("commit kafka message failed", zap.Int64("offset", msg.Offset), zap.Error(err))
			}
			if !c.waitRetry(ctx) {
				return
			}
		}
	}
	c.logger.Info("kafka consumer stopping")
}

func (c *Consumer) waitRetry(ctx context.Context) bool {
	timer := time.NewTimer(c.retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Close 关闭消费者
func (c *Consumer) Close() error {
	return c.reader.Close()
}
