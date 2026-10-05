package main

import (
	"context"
	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
)

// Task owns this row; published means Kafka acknowledged, not browser received.
type taskNotificationOutboxRow struct {
	NotificationID int64 `gorm:"primaryKey;autoIncrement:false"`
	TeamID         int64
	RecipientID    int64
	EventVersion   int
	Published      bool
}

func (taskNotificationOutboxRow) TableName() string { return "task_notification_outbox" }
func (r taskNotificationOutboxRow) Event() model.TaskNotificationEvent {
	return model.TaskNotificationEvent{Version: r.EventVersion, Type: model.TaskNotificationEventType, NotificationID: r.NotificationID, TeamID: r.TeamID, RecipientID: r.RecipientID}
}
func (r taskNotificationOutboxRow) Validate() error { return r.Event().Validate() }

type taskNotificationOutboxStore interface {
	ListPending(context.Context, int) ([]taskNotificationOutboxRow, error)
	MarkPublished(context.Context, taskNotificationOutboxRow) error
}

type taskNotificationWriter interface {
	WriteMessages(context.Context, ...kafka.Message) error
	Close() error
}
