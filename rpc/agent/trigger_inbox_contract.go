package agent

import (
	"context"
	"errors"
	"strconv"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const TriggerInboxQueued = "queued"

// Persisted notification only; Accept grants no permission to read or generate.
type TriggerInbox interface {
	Accept(context.Context, model.AgentTriggerEvent) error
}

// Fetch never acknowledges. The consumer commits only after Accept succeeds.
type TriggerEventReader interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
	Close() error
}

var ErrInvalidTriggerNotification = errors.New("invalid trigger notification; consumption stopped without acknowledgement")
var ErrInvalidTriggerInbox = errors.New("invalid persisted trigger notification; consumption stopped without acknowledgement")

func validateTriggerNotification(event model.AgentTriggerEvent) error {
	if event.MessageID <= 0 || event.Action != model.AgentTriggerAction || event.Version != model.AgentTriggerVersion {
		return status.Error(codes.InvalidArgument, "invalid trigger notification")
	}
	return nil
}

func triggerNotificationKey(event model.AgentTriggerEvent) string {
	return "agent-trigger:tasks:" + strconv.FormatInt(event.MessageID, 10)
}
