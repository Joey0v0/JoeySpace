package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TriggerWorker serially processes leased sources independently of Kafka intake
// and ordinary person RPCs. It never owns model retry or persistent budgets.
type TriggerWorker struct {
	store         TriggerExecutionStore
	processor     TriggerProcessor
	pollInterval  time.Duration
	renewInterval time.Duration
	cancelWait    time.Duration
	running       atomic.Bool
	abandoned     atomic.Bool
}

// NewTriggerWorker fixes polling and renewal intervals for the 30-second lease.
func NewTriggerWorker(store TriggerExecutionStore, processor TriggerProcessor) (*TriggerWorker, error) {
	if nilTriggerConsumerDependency(store) || nilTriggerConsumerDependency(processor) {
		return nil, errors.New("trigger worker store and processor are required")
	}
	return &TriggerWorker{store: store, processor: processor, pollInterval: 2 * time.Second, renewInterval: 10 * time.Second, cancelWait: 5 * time.Second}, nil
}

func (w *TriggerWorker) waitForProcess(done <-chan error) (error, bool) {
	delay := w.cancelWait
	if delay <= 0 {
		delay = 5 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, true
	case <-timer.C:
		// An uncooperative canceled processor must never overlap a later Run.
		w.abandoned.Store(true)
		return nil, false
	}
}

func (w *TriggerWorker) processLease(ctx context.Context, lease TriggerLease) error {
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.processor.Process(processCtx, lease) }()
	interval := w.renewInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			_, _ = w.waitForProcess(done)
			return ctx.Err()
		case processErr := <-done:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if processErr == nil {
				return nil
			}
			if errors.Is(processErr, ErrTriggerLeaseLost) || errors.Is(processErr, ErrInvalidTriggerState) {
				return status.Error(codes.Unavailable, "trigger worker lost its valid processing lease")
			}
			// Release rechecks live ownership atomically. It alone owns durable
			// retry timing and model budget; the worker does not reset either.
			err := w.store.Release(ctx, lease)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return status.Error(codes.Unavailable, "trigger worker could not release failed work")
			}
			return nil
		case <-ticker.C:
			renewed, err := w.store.Renew(ctx, lease)
			if ctx.Err() != nil {
				cancel()
				_, _ = w.waitForProcess(done)
				return ctx.Err()
			}
			if err == nil && (renewed == nil || renewed.MessageID != lease.MessageID || renewed.Token != lease.Token) {
				err = errors.New("invalid renewed trigger lease")
			}
			if err != nil {
				cancel()
				processErr, finished := w.waitForProcess(done)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Completion clears the lease. If Process already proved its
				// atomic commit succeeded, an overlapping renewal's lost-owner
				// result is expected; never release that completed source.
				if errors.Is(err, ErrTriggerLeaseLost) && finished && processErr == nil {
					return nil
				}
				return status.Error(codes.Unavailable, "trigger worker could not renew processing lease")
			}
			lease = *renewed
		}
	}
}

// Run is serial: a source finishes (or its canceled processing wait expires)
// before this worker can claim another. Neither RPC nor Kafka intake is owned.
func (w *TriggerWorker) Run(ctx context.Context) error {
	if ctx == nil {
		return status.Error(codes.InvalidArgument, "trigger worker context required")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if w == nil || nilTriggerConsumerDependency(w.store) || nilTriggerConsumerDependency(w.processor) {
		return status.Error(codes.Unavailable, "trigger worker is not configured")
	}
	if !w.running.CompareAndSwap(false, true) {
		return status.Error(codes.FailedPrecondition, "trigger worker is already running")
	}
	defer w.running.Store(false)
	if w.abandoned.Load() {
		return status.Error(codes.Unavailable, "trigger worker processing did not stop")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lease, err := w.store.Claim(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return status.Error(codes.Unavailable, "trigger worker could not claim work")
		}
		if lease == nil {
			interval := w.pollInterval
			if interval <= 0 {
				interval = 2 * time.Second
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if lease.MessageID <= 0 || !validTriggerLeaseToken(lease.Token) {
			return status.Error(codes.Unavailable, "trigger worker received an invalid lease")
		}
		if err := w.processLease(ctx, *lease); err != nil {
			return err
		}
	}
}
