package repository

import (
	"context"
	"github.com/yjydist/go-im/internal/model"
)

// AgentTriggerStore does not expose user tokens or a general user impersonation API.
type AgentTriggerStore interface {
	ListPending(context.Context, int) ([]model.AgentTriggerOutbox, error)
	MarkPublished(context.Context, model.AgentTriggerOutbox) error
}
