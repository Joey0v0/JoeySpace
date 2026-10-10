package ws

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/repository"
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
