package agent

import (
	"context"
	"errors"
	"time"
)

const (
	TriggerInboxRunning      = "running"
	TriggerInboxExhausted    = "exhausted"
	TriggerModelAttemptLimit = 2
	TriggerLeaseDuration     = 30 * time.Second
)

var (
	ErrTriggerLeaseLost    = errors.New("trigger execution lease is no longer valid")
	ErrInvalidTriggerState = errors.New("persisted trigger execution state is invalid")
)

// Token is an internal fencing capability, never an HTTP/RPC response or log field.
// Until and ModelAttempts are observations, not caller authority; SQL time and
// the saved token decide whether each mutation is allowed.
type TriggerLease struct {
	MessageID     int64
	Token         string
	Until         time.Time
	ModelAttempts int
	ModelStarted  bool
}

// Claim returns nil for an empty queue, or after retiring an expired final
// attempt. A later poll can continue. Claim and Renew do not spend model budget.
// BeginModel must commit before a model call. Only true permits that call;
// repeated/uncertain calls must not reuse a previously granted permission.
// Release keeps pre-model failures retryable and retires a spent final attempt.
type TriggerExecutionStore interface {
	Claim(context.Context) (*TriggerLease, error)
	Renew(context.Context, TriggerLease) (*TriggerLease, error)
	BeginModel(context.Context, TriggerLease) (bool, error)
	Release(context.Context, TriggerLease) error
}
