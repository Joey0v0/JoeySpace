package ws

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
)

type onlineLeaseFake struct {
	repository.RedisRepository
	mu    sync.Mutex
	owner string
	addr  string
}

func (r *onlineLeaseFake) SetOnlineLease(_ context.Context, _ int64, addr, owner string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner, r.addr = owner, addr
	return nil
}

func (r *onlineLeaseFake) RefreshOnlineLease(_ context.Context, _ int64, owner string, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner == owner, nil
}

func (r *onlineLeaseFake) DelOnlineLease(_ context.Context, _ int64, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owner == owner {
		r.owner, r.addr = "", ""
	}
	return nil
}

func TestReplacedSocketCannotEraseOrRefreshNewOnlineRoute(t *testing.T) {
	repo := &onlineLeaseFake{}
	old := &Client{UserID: 42, wsRPCAddr: "im-ws:9091", redisRepo: repo, onlineLease: "old"}
	newer := &Client{UserID: 42, wsRPCAddr: "im-ws:9091", redisRepo: repo, onlineLease: "new"}
	ctx := context.Background()
	if err := old.setOnline(ctx); err != nil {
		t.Fatal(err)
	}
	if err := newer.setOnline(ctx); err != nil {
		t.Fatal(err)
	}
	if current, err := old.refreshOnline(ctx); err != nil || current {
		t.Fatalf("stale refresh: current=%v err=%v", current, err)
	}
	if err := old.clearOnline(ctx); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	owner, addr := repo.owner, repo.addr
	repo.mu.Unlock()
	if owner != "new" || addr != "im-ws:9091" {
		t.Fatalf("new route lost: owner=%q addr=%q", owner, addr)
	}
	if current, err := newer.refreshOnline(ctx); err != nil || !current {
		t.Fatalf("new refresh: current=%v err=%v", current, err)
	}
	if err := newer.clearOnline(ctx); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	owner, addr = repo.owner, repo.addr
	repo.mu.Unlock()
	if owner != "" || addr != "" {
		t.Fatalf("new route survived close: owner=%q addr=%q", owner, addr)
	}
}

func TestLateOldSocketCannotPublishOverReplacement(t *testing.T) {
	repo := &onlineLeaseFake{}
	hub := NewHub(zap.NewNop())
	old := &Client{UserID: 42, wsRPCAddr: "im-ws:9091", redisRepo: repo, onlineLease: "old", closeCh: make(chan struct{})}
	newer := &Client{UserID: 42, wsRPCAddr: "im-ws:9091", redisRepo: repo, onlineLease: "new", closeCh: make(chan struct{})}
	hub.Register(old)
	// Model the old read loop finishing before its delayed Start publishes.
	old.closeOnce.Do(func() { close(old.closeCh) })
	hub.Register(newer)
	current, err := hub.PublishOnline(context.Background(), newer)
	if err != nil || !current {
		t.Fatalf("new publication: current=%v err=%v", current, err)
	}
	current, err = hub.PublishOnline(context.Background(), old)
	if err != nil || current {
		t.Fatalf("stale publication: current=%v err=%v", current, err)
	}
	repo.mu.Lock()
	owner := repo.owner
	repo.mu.Unlock()
	if owner != "new" {
		t.Fatalf("new route overwritten by stale socket: %q", owner)
	}
}

type blockingLeaseFake struct {
	onlineLeaseFake
	started chan struct{}
	release chan struct{}
}

func (r *blockingLeaseFake) SetOnlineLease(ctx context.Context, userID int64, addr, owner string, ttl time.Duration) error {
	close(r.started)
	select {
	case <-r.release:
		return r.onlineLeaseFake.SetOnlineLease(ctx, userID, addr, owner, ttl)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSlowLeasePublishDoesNotBlockAnotherUser(t *testing.T) {
	repo := &blockingLeaseFake{started: make(chan struct{}), release: make(chan struct{})}
	hub := NewHub(zap.NewNop())
	first := &Client{UserID: 42, redisRepo: repo, wsRPCAddr: "im-ws:9091", onlineLease: "first", closeCh: make(chan struct{})}
	other := &Client{UserID: 43, redisRepo: repo, wsRPCAddr: "im-ws:9091", onlineLease: "other", closeCh: make(chan struct{})}
	hub.Register(first)
	published := make(chan struct{})
	go func() {
		defer close(published)
		hub.PublishOnline(context.Background(), first)
	}()
	<-repo.started
	defer func() {
		close(repo.release)
		<-published
	}()
	done := make(chan struct{})
	go func() {
		hub.Register(other)
		_, _ = hub.GetClient(other.UserID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("another user's registration or lookup waited for Redis")
	}
}
