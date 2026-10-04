package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type workerTestStore struct {
	claim                      func(context.Context) (*TriggerLease, error)
	renew                      func(context.Context, TriggerLease) (*TriggerLease, error)
	release                    func(context.Context, TriggerLease) error
	claims, renewals, releases atomic.Int32
}

func (s *workerTestStore) Claim(ctx context.Context) (*TriggerLease, error) {
	s.claims.Add(1)
	return s.claim(ctx)
}
func (s *workerTestStore) Renew(ctx context.Context, lease TriggerLease) (*TriggerLease, error) {
	s.renewals.Add(1)
	if s.renew != nil {
		return s.renew(ctx, lease)
	}
	return &lease, nil
}
func (s *workerTestStore) Release(ctx context.Context, lease TriggerLease) error {
	s.releases.Add(1)
	if s.release != nil {
		return s.release(ctx, lease)
	}
	return nil
}
func (*workerTestStore) BeginModel(context.Context, TriggerLease) (bool, error) {
	panic("worker must not spend model budget")
}

type workerTestProcessor func(context.Context, TriggerLease) error

func (p workerTestProcessor) Process(ctx context.Context, lease TriggerLease) error {
	return p(ctx, lease)
}
func workerTestLease() TriggerLease {
	return TriggerLease{MessageID: 9007199254740993, Token: strings.Repeat("a", 64), Until: time.Now().Add(30 * time.Second)}
}
func newFastTestWorker(t *testing.T, store *workerTestStore, processor workerTestProcessor) *TriggerWorker {
	t.Helper()
	worker, err := NewTriggerWorker(store, processor)
	if err != nil {
		t.Fatal(err)
	}
	worker.pollInterval = 20 * time.Millisecond
	worker.renewInterval = 10 * time.Millisecond
	worker.cancelWait = 100 * time.Millisecond
	return worker
}
func waitTestWorker(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
		return nil
	}
}
func TestTriggerWorkerRequiresDependenciesAndCallerContext(t *testing.T) {
	var store *workerTestStore
	var processor workerTestProcessor
	for _, deps := range []struct {
		s TriggerExecutionStore
		p TriggerProcessor
	}{{nil, nil}, {store, workerTestProcessor(func(context.Context, TriggerLease) error { return nil })}, {&workerTestStore{}, processor}} {
		if w, err := NewTriggerWorker(deps.s, deps.p); err == nil || w != nil {
			t.Fatal("missing dependency accepted")
		}
	}
	var w *TriggerWorker
	if status.Code(w.Run(nil)) != codes.InvalidArgument {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(w.Run(ctx), context.Canceled) {
		t.Fatal("caller cancellation lost")
	}
}
func TestTriggerWorkerEmptyQueueWaitIsCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	store := &workerTestStore{claim: func(context.Context) (*TriggerLease, error) { called <- struct{}{}; return nil, nil }}
	w := newFastTestWorker(t, store, func(context.Context, TriggerLease) error { t.Error("empty queue processed"); return nil })
	w.pollInterval = time.Hour
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-called
	cancel()
	if !errors.Is(waitTestWorker(t, done), context.Canceled) || store.claims.Load() != 1 {
		t.Fatal("empty poll did not wait/cancel")
	}
}
func TestTriggerWorkerRenewsSeriallyAndReleasesLatestLeaseAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease := workerTestLease()
	updated := lease
	updated.Until = updated.Until.Add(time.Second)
	renewed := make(chan struct{}, 1)
	processed := make(chan struct{})
	store := &workerTestStore{}
	store.claim = func(context.Context) (*TriggerLease, error) {
		if store.claims.Load() == 1 {
			return &lease, nil
		}
		cancel()
		return nil, context.Canceled
	}
	store.renew = func(_ context.Context, l TriggerLease) (*TriggerLease, error) {
		if l.MessageID != lease.MessageID || l.Token != lease.Token {
			t.Error("renew changed identity")
		}
		select {
		case renewed <- struct{}{}:
		default:
		}
		return &updated, nil
	}
	store.release = func(_ context.Context, l TriggerLease) error {
		if l != updated {
			t.Error("release did not use latest observations")
		}
		return nil
	}
	w := newFastTestWorker(t, store, func(_ context.Context, l TriggerLease) error {
		if l != lease {
			t.Error("wrong claimed lease")
		}
		<-processed
		return status.Error(codes.PermissionDenied, "private source unavailable")
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-renewed:
	case <-time.After(time.Second):
		t.Fatal("no renewal")
	}
	if store.claims.Load() != 1 || status.Code(w.Run(context.Background())) != codes.FailedPrecondition {
		t.Fatal("processing overlapped another claim/Run")
	}
	close(processed)
	if !errors.Is(waitTestWorker(t, done), context.Canceled) || store.releases.Load() != 1 || store.claims.Load() != 2 {
		t.Fatal("failed source was not released then next claimed")
	}
}
func TestTriggerWorkerStorageFailuresStopAndDoNotLeak(t *testing.T) {
	for _, kind := range []string{"claim", "release", "lost_process", "invalid_lease"} {
		t.Run(kind, func(t *testing.T) {
			lease := workerTestLease()
			store := &workerTestStore{claim: func(context.Context) (*TriggerLease, error) { return &lease, nil }}
			processor := workerTestProcessor(func(context.Context, TriggerLease) error { return errors.New("private processor") })
			switch kind {
			case "claim":
				store.claim = func(context.Context) (*TriggerLease, error) { return nil, errors.New("mysql private-password") }
			case "release":
				store.release = func(context.Context, TriggerLease) error { return errors.New("private database") }
			case "lost_process":
				processor = func(context.Context, TriggerLease) error { return ErrTriggerLeaseLost }
			case "invalid_lease":
				lease.Token = "invalid"
			}
			err := newFastTestWorker(t, store, processor).Run(context.Background())
			if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") || store.claims.Load() != 1 {
				t.Fatalf("unsafe failure: %v", err)
			}
			if kind != "release" && store.releases.Load() != 0 {
				t.Fatal("invalid ownership released")
			}
		})
	}
}
func TestTriggerWorkerRenewalFailureCancelsWithoutRelease(t *testing.T) {
	for _, kind := range []string{"lost", "database", "nil", "different_owner"} {
		t.Run(kind, func(t *testing.T) {
			lease := workerTestLease()
			observedCancel := make(chan struct{})
			store := &workerTestStore{claim: func(context.Context) (*TriggerLease, error) { return &lease, nil }, renew: func(context.Context, TriggerLease) (*TriggerLease, error) {
				switch kind {
				case "lost":
					return nil, ErrTriggerLeaseLost
				case "database":
					return nil, errors.New("private mysql")
				case "different_owner":
					changed := lease
					changed.Token = strings.Repeat("b", 64)
					return &changed, nil
				default:
					return nil, nil
				}
			}}
			w := newFastTestWorker(t, store, func(ctx context.Context, _ TriggerLease) error { <-ctx.Done(); close(observedCancel); return ctx.Err() })
			if err := w.Run(context.Background()); status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
				t.Fatalf("renew failure: %v", err)
			}
			select {
			case <-observedCancel:
			default:
				t.Fatal("processor not canceled")
			}
			if store.claims.Load() != 1 || store.releases.Load() != 0 {
				t.Fatal("lost lease was released/reclaimed")
			}
		})
	}
}
func TestTriggerWorkerCommittedCompletionWinsOverOverlappingLostRenewal(t *testing.T) {
	for _, kind := range []string{"completed", "database_failure", "caller_cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lease := workerTestLease()
			complete := make(chan struct{})
			store := &workerTestStore{}
			store.claim = func(context.Context) (*TriggerLease, error) {
				if store.claims.Load() == 1 {
					return &lease, nil
				}
				cancel()
				return nil, context.Canceled
			}
			store.renew = func(context.Context, TriggerLease) (*TriggerLease, error) {
				close(complete)
				if kind == "caller_cancel" {
					cancel()
				}
				if kind == "database_failure" {
					return nil, errors.New("private storage")
				}
				return nil, ErrTriggerLeaseLost
			}
			w := newFastTestWorker(t, store, func(context.Context, TriggerLease) error { <-complete; return nil })
			err := w.Run(ctx)
			if kind == "database_failure" {
				if status.Code(err) != codes.Unavailable || store.claims.Load() != 1 {
					t.Fatal("storage fault masked by completion")
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("completion/cancel: %v", err)
			}
			if kind == "completed" && store.claims.Load() != 2 {
				t.Fatal("successful commit reported as lost-lease fault")
			}
			if kind == "caller_cancel" && store.claims.Load() != 1 {
				t.Fatal("canceled worker claimed again")
			}
			if store.releases.Load() != 0 {
				t.Fatal("committed source released")
			}
		})
	}
}
func TestTriggerWorkerCancellationWaitIsBoundedAndCannotRestartAbandonedProcessor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease := workerTestLease()
	started := make(chan struct{})
	finish := make(chan struct{})
	exit := make(chan struct{})
	var writeAfterCancellation atomic.Bool
	store := &workerTestStore{claim: func(context.Context) (*TriggerLease, error) { return &lease, nil }}
	w := newFastTestWorker(t, store, func(ctx context.Context, _ TriggerLease) error {
		close(started)
		<-finish
		if ctx.Err() == nil {
			writeAfterCancellation.Store(true)
		}
		close(exit)
		return ctx.Err()
	})
	w.cancelWait = 20 * time.Millisecond
	w.renewInterval = time.Hour
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	if !errors.Is(waitTestWorker(t, done), context.Canceled) {
		t.Fatal("active cancellation reported success")
	}
	if status.Code(w.Run(context.Background())) != codes.Unavailable || store.claims.Load() != 1 || store.releases.Load() != 0 {
		t.Fatal("abandoned processor overlapped new claim/release")
	}
	close(finish)
	<-exit
	if writeAfterCancellation.Load() {
		t.Fatal("processing context was not canceled")
	}
}

func TestTriggerWorkerDeadlineWinsOverProcessorAndStorageResults(t *testing.T) {
	for _, stage := range []string{"claim", "process", "release"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			lease := workerTestLease()
			store := &workerTestStore{claim: func(context.Context) (*TriggerLease, error) { return &lease, nil }}
			processor := workerTestProcessor(func(ctx context.Context, _ TriggerLease) error { <-ctx.Done(); return nil })
			switch stage {
			case "claim":
				store.claim = func(ctx context.Context) (*TriggerLease, error) {
					<-ctx.Done()
					return &lease, errors.New("private store")
				}
			case "release":
				processor = func(context.Context, TriggerLease) error { return errors.New("private failure") }
				store.release = func(ctx context.Context, _ TriggerLease) error { <-ctx.Done(); return errors.New("private store") }
			}
			worker := newFastTestWorker(t, store, processor)
			worker.renewInterval = time.Hour
			if err := worker.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline lost at %s: %v", stage, err)
			}
			if store.claims.Load() != 1 || stage != "release" && store.releases.Load() != 0 {
				t.Fatal("expired worker continued or released canceled work")
			}
		})
	}
}

var _ TriggerExecutionStore = (*workerTestStore)(nil)
var _ TriggerProcessor = workerTestProcessor(nil)
