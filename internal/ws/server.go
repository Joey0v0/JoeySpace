package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/rpc/im/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// upgrader WebSocket 升级配置
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// HandleWS performs the configured Origin check before calling Upgrade.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsTicketStore interface {
	Issue(context.Context, int64, string, time.Duration) (string, error)
	Consume(context.Context, string) (int64, string, error)
}

// Server WebSocket 网关服务
type Server struct {
	hub            *Hub
	kafkaWriter    *kafkaWriterAdapter
	redisRepo      repository.RedisRepository
	wsRPCAddr      string
	jwtSecret      string
	imConn         *grpc.ClientConn
	imClient       pb.IMClient
	logger         *zap.Logger
	tickets        wsTicketStore
	allowedOrigins map[string]struct{}
}

// kafkaWriterAdapter 适配 kafka-go Writer 到 KafkaWriter 接口
type kafkaWriterAdapter struct {
	writer *kafka.Writer
}

func (a *kafkaWriterAdapter) WriteMessages(ctx context.Context, msgs ...KafkaMessage) error {
	kafkaMsgs := make([]kafka.Message, len(msgs))
	for i, m := range msgs {
		kafkaMsgs[i] = kafka.Message{
			Key:   m.Key,
			Value: m.Value,
		}
	}
	return a.writer.WriteMessages(ctx, kafkaMsgs...)
}

// NewServer 创建 WS 网关服务
func NewServer(cfg *config.Config, hub *Hub, redisRepo repository.RedisRepository, logger *zap.Logger) *Server {
	// 创建 Kafka Writer
	writer := kafka.NewWriter(kafka.WriterConfig{
		Brokers:  cfg.Kafka.Brokers,
		Topic:    cfg.Kafka.TopicChat,
		Balancer: &kafka.LeastBytes{},
	})

	wsRPCAddr := os.Getenv("WS_RPC_ADDR")
	if wsRPCAddr == "" {
		wsRPCAddr = fmt.Sprintf("localhost:%d", cfg.WSServer.RPCPort)
	}
	var imConn *grpc.ClientConn
	var imClient pb.IMClient
	if addr := os.Getenv("IM_RPC_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			logger.Error("create IM RPC client failed", zap.Error(err))
		} else {
			imConn = conn
			imClient = pb.NewIMClient(conn)
		}
	}

	return &Server{
		hub:            hub,
		kafkaWriter:    &kafkaWriterAdapter{writer: writer},
		redisRepo:      redisRepo,
		wsRPCAddr:      wsRPCAddr,
		jwtSecret:      cfg.JWT.Secret,
		imConn:         imConn,
		imClient:       imClient,
		logger:         logger,
		tickets:        repository.NewWSTicketStore(),
		allowedOrigins: parseAllowedOrigins(os.Getenv("WS_ALLOWED_ORIGINS")),
	}
}

const wsTicketTTL = 30 * time.Second

func parseAllowedOrigins(raw string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin != "" && origin != "*" {
			allowed[origin] = struct{}{}
		}
	}
	return allowed
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	} // existing non-browser clients
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	_, ok := s.allowedOrigins[origin]
	return ok
}

// HandleWSTicket exchanges a valid Bearer token for a short-lived, single-use URL credential.
func (s *Server) HandleWSTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.originAllowed(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	claims, err := jwt.ParseToken(parts[1], s.jwtSecret)
	if err != nil || claims.UserID <= 0 {
		http.Error(w, "invalid bearer token", http.StatusUnauthorized)
		return
	}
	if s.tickets == nil {
		http.Error(w, "ticket service unavailable", http.StatusServiceUnavailable)
		return
	}
	ticket, err := s.tickets.Issue(r.Context(), claims.UserID, parts[1], wsTicketTTL)
	if err != nil {
		http.Error(w, "ticket service unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"ticket": ticket, "expires_in_seconds": 30}})
}

// HandleWS 处理 WebSocket 升级请求
// Formal browser clients use a single-use ticket. token remains for old clients.
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	if len(query["ticket"]) > 1 || len(query["token"]) > 1 || (query.Has("ticket") && query.Has("token")) {
		http.Error(w, "invalid credentials", http.StatusBadRequest)
		return
	}
	token := query.Get("token")
	if query.Has("ticket") {
		if s.tickets == nil {
			http.Error(w, "ticket service unavailable", http.StatusServiceUnavailable)
			return
		}
		userID, original, err := s.tickets.Consume(r.Context(), query.Get("ticket"))
		if errors.Is(err, repository.ErrWSTicketMissing) {
			http.Error(w, "invalid ticket", http.StatusUnauthorized)
			return
		}
		if err != nil {
			http.Error(w, "ticket service unavailable", http.StatusServiceUnavailable)
			return
		}
		claims, err := jwt.ParseToken(original, s.jwtSecret)
		if err != nil || claims.UserID != userID {
			http.Error(w, "invalid ticket", http.StatusUnauthorized)
			return
		}
		token = original
	}
	if token == "" {
		http.Error(w, "missing credentials", http.StatusUnauthorized)
		return
	}

	// 验证 JWT
	claims, err := jwt.ParseToken(token, s.jwtSecret)
	if err != nil || claims.UserID <= 0 {
		s.logger.Warn("ws auth failed",
			zap.String("remote_addr", r.RemoteAddr),
			zap.Error(err),
		)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// 升级为 WebSocket 连接
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ws upgrade failed",
			zap.Int64("user_id", claims.UserID),
			zap.Error(err),
		)
		return
	}

	s.logger.Info("ws connection established",
		zap.Int64("user_id", claims.UserID),
		zap.String("remote_addr", r.RemoteAddr),
	)

	// 创建 Client 并启动
	client := NewClient(claims.UserID, token, conn, s.hub, s.kafkaWriter, s.redisRepo, s.imClient, s.wsRPCAddr, s.logger)
	s.hub.Register(client)
	client.Start()
}

// HandleInternalPush 处理内部推送请求
// Push 服务通过此接口将消息推送给在线用户
// POST /internal/push
// Body: {"user_id": 123, "data": {...}}
func (s *Server) HandleInternalPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg PushMsg
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 通过 Hub 查找对应用户的连接
	client, ok := s.hub.GetClient(msg.UserID)
	if !ok {
		// 用户不在本节点
		http.Error(w, "user not connected", http.StatusNotFound)
		return
	}

	// 将内部消息中的整数 ID 精确转换为浏览器可安全解析的字符串。
	var chat model.Message
	if err := json.Unmarshal(msg.Data, &chat); err != nil {
		http.Error(w, "invalid chat data", http.StatusBadRequest)
		return
	}
	// 序列化下行消息
	serverMsg := ServerMsg{
		Type: "chat",
		Data: browserChatMessage(chat),
	}
	data, err := json.Marshal(serverMsg)
	if err != nil {
		s.logger.Error("marshal push msg failed",
			zap.Int64("user_id", msg.UserID),
			zap.Error(err),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 仅在成功进入写队列时确认，失败则让 Push 保存离线消息。
	if !client.Send(data) {
		http.Error(w, "client send queue unavailable", http.StatusServiceUnavailable)
		return
	}

	s.logger.Debug("pushed message to client",
		zap.Int64("user_id", msg.UserID),
	)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"code":0,"msg":"ok"}`))
}

// Close 关闭 Kafka Writer
func (s *Server) Close() error {
	if s.imConn != nil {
		s.imConn.Close()
	}
	return s.kafkaWriter.writer.Close()
}
