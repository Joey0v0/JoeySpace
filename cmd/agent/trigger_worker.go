package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	agent "github.com/yjydist/go-im/rpc/agent"
	"gorm.io/gorm"
)

type agentTriggerWorkerConfig struct{ Enabled bool }

func loadAgentTriggerWorkerConfig(getenv func(string) string) (agentTriggerWorkerConfig, error) {
	if getenv == nil {
		return agentTriggerWorkerConfig{}, errors.New("Agent trigger environment reader is required")
	}
	switch getenv("AGENT_TRIGGER_WORKER_ENABLED") {
	case "", "false":
		return agentTriggerWorkerConfig{}, nil
	case "true":
		return agentTriggerWorkerConfig{Enabled: true}, nil
	default:
		return agentTriggerWorkerConfig{}, errors.New("invalid Agent trigger worker switch")
	}
}

type agentTriggerSourceFactory func(func(string) string) (*agent.TriggerContextClient, error)

// Validate/load the dedicated certificates before DB or model resources. Off
// leaves the existing manual RPC path and every trigger resource untouched.
func prepareAgentTriggerWorkerSource(cfg agentTriggerWorkerConfig, getenv func(string) string, factory agentTriggerSourceFactory) (*agent.TriggerContextClient, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if factory == nil {
		factory = agent.NewTriggerContextClient
	}
	source, err := factory(getenv)
	if err != nil || source == nil {
		_ = source.Close()
		return nil, errors.New("enabled Agent trigger worker requires complete IM trigger mTLS configuration")
	}
	return source, nil
}

type agentTriggerProcessorFactory func(*agent.Server, *agent.TriggerContextClient, *agent.TriggerInboxStore) (agent.TriggerProcessor, error)
type agentTriggerWorkerRunner interface{ Run(context.Context) error }

type agentTriggerWorkerRuntime struct {
	worker           agentTriggerWorkerRunner
	closeClient      func() error
	stopWait         time.Duration
	mu               sync.Mutex
	started, stopped bool
	cancel           context.CancelFunc
	done             chan struct{}
	runError         error
	stopOnce         sync.Once
	stopErr          error
}

func newAgentTriggerWorker(cfg agentTriggerWorkerConfig, server *agent.Server, source *agent.TriggerContextClient, db *gorm.DB, factory agentTriggerProcessorFactory) (*agentTriggerWorkerRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if server == nil || source == nil || db == nil {
		return nil, errors.New("Agent trigger worker server, source and database are required")
	}
	if factory == nil {
		factory = agent.NewTriggerTaskProcessor
	}
	store := agent.NewTriggerInboxStore(db)
	processor, err := factory(server, source, store)
	if err != nil {
		return nil, errors.New("cannot prepare Agent trigger task processor")
	}
	worker, err := agent.NewTriggerWorker(store, processor)
	if err != nil {
		return nil, errors.New("cannot prepare Agent trigger worker")
	}
	return &agentTriggerWorkerRuntime{worker: worker, closeClient: source.Close, stopWait: 10 * time.Second}, nil
}

func nilAgentTriggerWorkerRunner(worker agentTriggerWorkerRunner) bool {
	if worker == nil {
		return true
	}
	v := reflect.ValueOf(worker)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan:
		return v.IsNil()
	}
	return false
}

func (r *agentTriggerWorkerRuntime) Start(parent context.Context, report func(error)) error {
	if r == nil {
		return nil
	}
	if parent == nil || nilAgentTriggerWorkerRunner(r.worker) {
		return errors.New("Agent trigger worker context and runner are required")
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return errors.New("Agent trigger worker cannot be started again")
	}
	ctx, cancel := context.WithCancel(parent)
	r.started, r.cancel, r.done = true, cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.worker.Run(ctx)
		if ctx.Err() == nil {
			failure := errors.New("Agent trigger worker stopped; check configuration and restart after repair")
			r.mu.Lock()
			r.runError = failure
			r.mu.Unlock()
			if report != nil {
				report(failure)
			}
		}
	}()
	return nil
}

func (r *agentTriggerWorkerRuntime) stoppedError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runError
}

// Cancellation precedes waiting and source closure. A faulty uncooperative
// processor cannot indefinitely block process shutdown or be restarted here.
func (r *agentTriggerWorkerRuntime) Stop() error {
	if r == nil {
		return nil
	}
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		cancel, done := r.cancel, r.done
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			delay := r.stopWait
			if delay <= 0 {
				delay = 10 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-done:
			case <-timer.C:
				r.stopErr = errors.New("Agent trigger worker cancellation did not finish within shutdown limit")
			}
			timer.Stop()
		}
		if r.closeClient != nil {
			if err := r.closeClient(); err != nil && r.stopErr == nil {
				r.stopErr = errors.New("cannot close Agent trigger IM context client")
			}
		}
	})
	return r.stopErr
}
