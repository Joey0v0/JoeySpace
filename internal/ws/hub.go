package ws

import (
	"sync"

	"go.uber.org/zap"
)

// Hub 连接管理器，维护所有在线客户端
type Hub struct {
	// clients 存储所有在线客户端，key 为 userID
	clients map[int64]*Client
	mu      sync.RWMutex

	// register 注册通道
	register chan *Client
	// unregister 注销通道
	unregister chan *Client

	logger *zap.Logger
}

// NewHub 创建连接管理器
func NewHub(logger *zap.Logger) *Hub {
	return &Hub{
		clients:    make(map[int64]*Client),
		register:   make(chan *Client, 256),
		unregister: make(chan *Client, 256),
		logger:     logger,
	}
}

// Run 启动 Hub，监听注册和注销事件
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			// 如果该用户已有旧连接，关闭旧连接
			if old, ok := h.clients[client.UserID]; ok {
				old.Close()
				h.logger.Info("replaced old connection",
					zap.Int64("user_id", client.UserID),
				)
			}
			h.clients[client.UserID] = client
			h.mu.Unlock()
			h.logger.Info("client registered",
				zap.Int64("user_id", client.UserID),
			)

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
	h.register <- client
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
