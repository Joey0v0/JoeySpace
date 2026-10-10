package ws

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Hub 连接管理器，维护所有在线客户端
type Hub struct {
	// clients 存储所有在线客户端，key 为 userID
	clients map[int64]*Client
	mu      sync.RWMutex
	locks   map[int64]*userPublishLock

	// unregister 注销通道
	unregister chan *Client

	logger *zap.Logger
}

type userPublishLock struct {
	mu   sync.Mutex
	refs int
}

// NewHub 创建连接管理器
func NewHub(logger *zap.Logger) *Hub {
	return &Hub{
		clients:    make(map[int64]*Client),
		locks:      make(map[int64]*userPublishLock),
		unregister: make(chan *Client, 256),
		logger:     logger,
	}
}

// Run 启动 Hub，监听注册和注销事件
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.unregister:
			h.mu.Lock()
			// 只有当前连接和注册的一致时才删除
			if cur, ok := h.clients[client.UserID]; ok && cur == client {
				delete(h.clients, client.UserID)
			}
			h.mu.Unlock()
			h.logger.Info("client unregistered",
				zap.Int64("user_id", client.UserID),
			)
		}
	}
}

// GetClient 获取指定用户的连接
func (h *Hub) GetClient(userID int64) (*Client, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	client, ok := h.clients[userID]
	return client, ok
}

// Register 注册客户端
func (h *Hub) Register(client *Client) {
	unlockUser := h.lockUser(client.UserID)
	defer unlockUser()
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.clients[client.UserID]; ok {
		old.Close()
		h.logger.Info("replaced old connection", zap.Int64("user_id", client.UserID))
	}
	h.clients[client.UserID] = client
	h.logger.Info("client registered", zap.Int64("user_id", client.UserID))
}

// PublishOnline serializes lease publication with connection replacement.
func (h *Hub) PublishOnline(ctx context.Context, client *Client) (bool, error) {
	unlockUser := h.lockUser(client.UserID)
	defer unlockUser()
	h.mu.RLock()
	current := h.clients[client.UserID] == client
	h.mu.RUnlock()
	if !current {
		return false, nil
	}
	select {
	case <-client.closeCh:
		return false, nil
	default:
	}
	publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return true, client.setOnline(publishCtx)
}

// lockUser orders registration and publication for one user without holding
// the Hub's client-map lock during Redis I/O. References keep a queued lock
// alive until every waiter has finished.
func (h *Hub) lockUser(userID int64) func() {
	h.mu.Lock()
	lock := h.locks[userID]
	if lock == nil {
		lock = &userPublishLock{}
		h.locks[userID] = lock
	}
	lock.refs++
	h.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		h.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(h.locks, userID)
		}
		h.mu.Unlock()
	}
}

// Unregister 注销客户端
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// OnlineCount 返回在线人数
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
